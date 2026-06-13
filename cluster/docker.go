package cluster

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// The Docker sidecar: a docker:dind VM managed by k3c, exposing a real
// Docker Engine API for Testcontainers, the docker CLI, and friends.
// Apple containers are full VMs, so dind runs natively (own kernel,
// cgroups, overlayfs). Pulls go through the k3c proxy; docker.io
// additionally through the pull cache as a registry mirror. The image
// store lives on a named volume and survives sidecar recreation.

const dockerName = "k3c-docker"
const dockerVolume = "k3c-docker-data"
const dockerImage = "docker.io/library/docker:dind"

// DockerUp starts (creating if needed) the Docker sidecar.
func DockerUp(cfg *config.Config) error {
	if err := preflight(); err != nil {
		return err
	}
	// match the cluster behavior: run the sidecar on a capable kernel
	EnsureRecommendedKernel()
	// the sidecar pulls through the host proxy and pull-cache mirror, and
	// its published ports are mirrored to the host — all served by the
	// daemons, so ensure they run even without a cluster
	if err := SpawnDaemons(cfg); err != nil {
		return err
	}
	if containerExists(dockerName, true) {
		logger.Info("docker sidecar already running")
		return dockerReady(cfg)
	}
	if containerExists(dockerName, false) {
		logger.Info("starting docker sidecar")
		// the per-VM netstack exits when its VM stops, so respawn it before
		// re-attaching the (already configured) gvnet network
		if cfg.TransparentEgress {
			if _, err := ensureGvnet(cfg, dockerName); err != nil {
				return err
			}
		}
		if out, err := runContainer("start", dockerName); err != nil {
			return fmt.Errorf("starting docker sidecar: %s", out)
		}
		applyCPUPriority(&config.Config{ServerName: dockerName, CPUPriority: cfg.CPUPriority})
		return dockerAwait(cfg)
	}

	if out, err := runContainer("volume", "inspect", dockerVolume); err != nil {
		logger.Info("creating docker image store volume (" + dockerVolume + ")")
		if out, err = runContainer("volume", "create", dockerVolume); err != nil {
			return fmt.Errorf("creating volume: %s", out)
		}
	}

	// the corporate TLS interception signs with the corporate CA: give
	// dockerd the same trust bundle the cluster node uses
	certDir := filepath.Join(cfg.BaseDir, "docker")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return err
	}
	bundle, err := caBundle(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(certDir, "ca-bundle.pem"), bundle, 0o644); err != nil {
		return err
	}

	logger.Info(fmt.Sprintf("starting docker sidecar (%s cpus, %s memory)", cfg.DockerCPUs, cfg.DockerMemory))
	args := []string{"run", "-d",
		"--name", dockerName,
		"--cap-add", "ALL",
		"--rosetta",
		"-m", cfg.DockerMemory,
		"-c", cfg.DockerCPUs,
		"-v", dockerVolume + ":/var/lib/docker",
		"-v", certDir + ":/k3c-ca",
		"-e", "SSL_CERT_FILE=/k3c-ca/ca-bundle.pem",
		// plain TCP engine API; TLS adds nothing on the local vmnet
		"-e", "DOCKER_TLS_CERTDIR=",
		"-p", "127.0.0.1:" + cfg.DockerPort + ":2375",
	}
	if cfg.TransparentEgress {
		// dual-NIC: vmnet stays primary (host<->VM: published 2375 + the
		// DockerHost IP keep targeting the host-routable vmnet NIC); the gvnet
		// NIC is added second and the entrypoint repoints the default route at
		// it for transparent egress. No CONNECT proxy needed.
		nets, err := gvnetNetworks(cfg, dockerName)
		if err != nil {
			return err
		}
		args = append(args, nets...)
	} else {
		proxyURL := fmt.Sprintf("http://%s:%s", cfg.VmnetGateway, cfg.ProxyPort)
		args = append(args,
			"-e", "HTTP_PROXY="+proxyURL,
			"-e", "HTTPS_PROXY="+proxyURL,
			"-e", "NO_PROXY="+cfg.NoProxy(),
		)
	}
	dockerd := []string{
		"dockerd",
		"--host=tcp://0.0.0.0:2375",
		"--host=unix:///var/run/docker.sock",
	}
	if cfg.PullCacheEnabled {
		// docker.io pulls through the shared pull cache (the cache
		// defaults the mirror namespace to docker.io)
		dockerd = append(dockerd, "--registry-mirror=http://"+cfg.VmnetGateway+":"+cfg.PullCachePort)
	}
	if cfg.RegistryEnabled {
		dockerd = append(dockerd, "--insecure-registry="+cfg.VmnetGateway+":"+cfg.RegistryPort)
	}
	if cfg.TransparentEgress {
		// repoint the default route at the gvnet NIC, then hand off to the dind
		// entrypoint (which prepares the engine) — keeps egress transparent
		args = append(args, "--entrypoint", "/bin/sh", dockerImage, "-c",
			config.GvnetRouteSnippet+"exec dockerd-entrypoint.sh "+strings.Join(dockerd, " "))
	} else {
		args = append(args, dockerImage)
		args = append(args, dockerd...)
	}
	if out, err := runContainer(args...); err != nil {
		return fmt.Errorf("docker sidecar start failed: %s", out)
	}
	applyCPUPriority(&config.Config{ServerName: dockerName, CPUPriority: cfg.CPUPriority})
	return dockerAwait(cfg)
}

// dockerAwait waits until the engine answers, then finalizes the sidecar.
func dockerAwait(cfg *config.Config) error {
	logger.Info("waiting for the docker engine")
	for i := 0; i < 60; i++ {
		if out, err := runContainer("exec", dockerName, "docker", "version", "--format", "{{.Server.Version}}"); err == nil {
			logger.Info("docker engine " + firstLine(out) + " ready")
			return dockerReady(cfg)
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("docker engine did not become ready; check: k3c container logs %s", dockerName)
}

// dockerReady activates the docker context (so docker and Testcontainers
// use the sidecar automatically) and reports how to reach it.
func dockerReady(cfg *config.Config) error {
	host, err := DockerHost(cfg)
	if err != nil {
		return err
	}
	if ensureDockerContext(cfg, host) {
		logger.Info("docker context '" + cfg.DockerContext + "' active")
		return nil
	}
	// no context (docker CLI absent or disabled): fall back to env
	fmt.Println("export DOCKER_HOST=" + host)
	fmt.Println("# activate with: eval $(k3c docker env)")
	return nil
}

// ensureDockerContext creates or updates the k3c docker context to point
// at the sidecar and makes it the active context. Returns false when
// context management is disabled or the docker CLI is unavailable, so the
// caller can fall back to DOCKER_HOST. The sidecar IP changes across
// recreates, so the host is refreshed on every up.
func ensureDockerContext(cfg *config.Config, host string) bool {
	name := cfg.DockerContext
	if name == "" || name == "off" {
		return false
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	if _, err := runOut("docker", "context", "inspect", name); err == nil {
		if out, err := runOut("docker", "context", "update", name, "--docker", "host="+host); err != nil {
			logger.Warn("updating docker context: " + out)
			return false
		}
	} else {
		if out, err := runOut("docker", "context", "create", name,
			"--description", "k3c docker sidecar", "--docker", "host="+host); err != nil {
			logger.Warn("creating docker context: " + out)
			return false
		}
	}
	if out, err := runOut("docker", "context", "use", name); err != nil {
		logger.Warn("activating docker context: " + out)
		return false
	}
	return true
}

// restoreDockerContext switches the docker CLI back to the default context
// when our context is the active one, so stopping the sidecar does not
// leave the CLI pointed at a dead engine.
func restoreDockerContext(cfg *config.Config) {
	name := cfg.DockerContext
	if name == "" || name == "off" {
		return
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return
	}
	current, err := runOut("docker", "context", "show")
	if err != nil || strings.TrimSpace(current) != name {
		return // the user is on a different context; leave it alone
	}
	if out, err := runOut("docker", "context", "use", "default"); err != nil {
		logger.Warn("restoring docker context: " + out)
		return
	}
	logger.Info("docker context restored to 'default'")
}

// DockerHost returns the engine endpoint. The sidecar VM's address is
// used (not the published localhost port): Testcontainers and friends
// connect to mapped container ports on the DOCKER_HOST address, and those
// are only served on the VM.
func DockerHost(cfg *config.Config) (string, error) {
	ip := containerIP(dockerName)
	if ip == "" {
		return "", fmt.Errorf("docker sidecar is not running (k3c docker up)")
	}
	return "tcp://" + ip + ":2375", nil
}

// DockerEnv prints shell exports for the sidecar engine.
func DockerEnv(cfg *config.Config) error {
	host, err := DockerHost(cfg)
	if err != nil {
		return err
	}
	fmt.Println("export DOCKER_HOST=" + host)
	fmt.Println("export TESTCONTAINERS_RYUK_DISABLED=false")
	return nil
}

// DockerDown stops the sidecar (the image store volume stays) and restores
// the default docker context.
func DockerDown(cfg *config.Config) error {
	if !containerExists(dockerName, false) {
		return fmt.Errorf("docker sidecar does not exist")
	}
	if out, err := runContainer("stop", dockerName); err != nil {
		return fmt.Errorf("stopping docker sidecar: %s", out)
	}
	if cfg.TransparentEgress {
		stopGvnet(cfg, dockerName)
	}
	restoreDockerContext(cfg)
	logger.Info("docker sidecar stopped (image store kept; k3c docker up restarts it)")
	return nil
}

// DockerStatus prints the sidecar state and the active docker context.
func DockerStatus(cfg *config.Config) error {
	switch {
	case containerExists(dockerName, true):
		fmt.Println("docker sidecar: running")
		if host, err := DockerHost(cfg); err == nil {
			fmt.Println("  host:    " + host)
		}
		if name := cfg.DockerContext; name != "" && name != "off" {
			if current, err := runOut("docker", "context", "show"); err == nil {
				active := "inactive"
				if strings.TrimSpace(current) == name {
					active = "active"
				}
				fmt.Println("  context: " + name + " (" + active + ")")
			}
		}
	case containerExists(dockerName, false):
		fmt.Println("docker sidecar: stopped (k3c docker up starts it)")
	default:
		fmt.Println("docker sidecar: not created (k3c docker up creates it)")
	}
	return nil
}
