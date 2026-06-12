package cluster

import (
	"fmt"
	"os"
	"path/filepath"
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
	if containerExists(dockerName, true) {
		logger.Info("docker sidecar already running")
		return dockerPrintHost(cfg)
	}
	if containerExists(dockerName, false) {
		logger.Info("starting docker sidecar")
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
	proxyURL := fmt.Sprintf("http://%s:%s", cfg.VmnetGateway, cfg.ProxyPort)
	args := []string{"run", "-d",
		"--name", dockerName,
		"--cap-add", "ALL",
		"--rosetta",
		"-m", cfg.DockerMemory,
		"-c", cfg.DockerCPUs,
		"-v", dockerVolume + ":/var/lib/docker",
		"-v", certDir + ":/k3c-ca",
		"-e", "SSL_CERT_FILE=/k3c-ca/ca-bundle.pem",
		"-e", "HTTP_PROXY=" + proxyURL,
		"-e", "HTTPS_PROXY=" + proxyURL,
		"-e", "NO_PROXY=" + cfg.NoProxy(),
		// plain TCP engine API; TLS adds nothing on the local vmnet
		"-e", "DOCKER_TLS_CERTDIR=",
		"-p", "127.0.0.1:" + cfg.DockerPort + ":2375",
		dockerImage,
		"dockerd",
		"--host=tcp://0.0.0.0:2375",
		"--host=unix:///var/run/docker.sock",
	}
	if cfg.PullCacheEnabled {
		// docker.io pulls through the shared pull cache (the cache
		// defaults the mirror namespace to docker.io)
		args = append(args, "--registry-mirror=http://"+cfg.VmnetGateway+":"+cfg.PullCachePort)
	}
	if cfg.RegistryEnabled {
		args = append(args, "--insecure-registry="+cfg.VmnetGateway+":"+cfg.RegistryPort)
	}
	if out, err := runContainer(args...); err != nil {
		return fmt.Errorf("docker sidecar start failed: %s", out)
	}
	applyCPUPriority(&config.Config{ServerName: dockerName, CPUPriority: cfg.CPUPriority})
	return dockerAwait(cfg)
}

// dockerAwait waits until the engine answers, then prints the endpoint.
func dockerAwait(cfg *config.Config) error {
	logger.Info("waiting for the docker engine")
	for i := 0; i < 60; i++ {
		if out, err := runContainer("exec", dockerName, "docker", "version", "--format", "{{.Server.Version}}"); err == nil {
			logger.Info("docker engine " + firstLine(out) + " ready")
			return dockerPrintHost(cfg)
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("docker engine did not become ready; check: k3c container logs %s", dockerName)
}

func dockerPrintHost(cfg *config.Config) error {
	host, err := DockerHost(cfg)
	if err != nil {
		return err
	}
	fmt.Println("export DOCKER_HOST=" + host)
	fmt.Println("# activate with: eval $(k3c docker env)")
	return nil
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

// DockerDown stops the sidecar (the image store volume stays).
func DockerDown(cfg *config.Config) error {
	if !containerExists(dockerName, false) {
		return fmt.Errorf("docker sidecar does not exist")
	}
	if out, err := runContainer("stop", dockerName); err != nil {
		return fmt.Errorf("stopping docker sidecar: %s", out)
	}
	logger.Info("docker sidecar stopped (image store kept; k3c docker up restarts it)")
	return nil
}

// DockerStatus prints the sidecar state.
func DockerStatus(cfg *config.Config) error {
	switch {
	case containerExists(dockerName, true):
		fmt.Println("docker sidecar: running")
		return dockerPrintHost(cfg)
	case containerExists(dockerName, false):
		fmt.Println("docker sidecar: stopped (k3c docker up starts it)")
	default:
		fmt.Println("docker sidecar: not created (k3c docker up creates it)")
	}
	return nil
}
