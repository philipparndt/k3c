package cluster

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
	"k3c/runtime"
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

// DockerUp starts (creating if needed) the Docker sidecar. When recreate is
// set and the sidecar already exists, it is removed first so it is created
// fresh — used by `docker up --cpus/--memory`, since a VM's resources are
// fixed at creation. The image-store volume is preserved.
func DockerUp(cfg *config.Config, recreate bool) error {
	if err := preflight(); err != nil {
		return err
	}
	// a paused sidecar's engine cannot answer (dockerReady would hang); lift
	// any freeze first so `docker up` on a paused sidecar just resumes it
	dockerResumeIfPaused(cfg)
	// match the cluster behavior: run the sidecar on the managed kernel
	EnsureClusterKernel(cfg)
	// the sidecar pulls through the host proxy and pull-cache mirror, and
	// its published ports are mirrored to the host — all served by the
	// daemons, so ensure they run even without a cluster
	if err := SpawnDaemons(cfg); err != nil {
		return err
	}
	if recreate && containerExists(dockerName, false) {
		logger.Info(fmt.Sprintf("recreating docker sidecar (%s cpus, %s memory)", cfg.DockerCPUs, cfg.DockerMemory))
		if out, err := runContainer("rm", "-f", dockerName); err != nil {
			return fmt.Errorf("removing docker sidecar: %s", out)
		}
		if cfg.TransparentEgress {
			removeGvnet(cfg, dockerName)
		}
	}
	if containerExists(dockerName, true) {
		logger.Info("docker sidecar already running")
		applyMemoryPolicy(cfg, dockerName)
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
		// sidecars created before runtime memory-policy support carry no
		// policy in their configuration; arm it for this run
		applyMemoryPolicy(cfg, dockerName)
		return dockerAwait(cfg)
	}

	if err := ensureDockerVolume(dockerVolume); err != nil {
		return err
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
		// bridge the in-guest nested-port forwarder's socket to the host (over
		// vsock, runtime-managed) so the host reaches published container ports
		// without dialing the guest vmnet IP (Phase 2, see ensureDockerForwarder)
		"--publish-socket", dockerForwardSocketPath(cfg) + ":" + guestForwardSocket,
	}
	// runtime-managed balloon: the footprint follows the workload
	args = append(args, memoryPolicyCreateArgs(cfg)...)
	if cfg.TransparentEgress {
		// dual-NIC: vmnet stays primary for the sidecar's gateway services
		// (proxy, pull-cache, registry at the vmnet gateway) and the cluster's
		// containerIP/kube-API; the gvnet NIC is added second and the entrypoint
		// repoints the default route at it for transparent egress. No CONNECT
		// proxy needed. The host reaches the engine via the Apple-published
		// 127.0.0.1:<DockerPort> loopback, not the guest vmnet IP (startDockerSocket).
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
	// Always enter through /bin/sh so the CA-trust prelude installs the mounted
	// bundle into the guest OS trust store (covering BuildKit/containerd, not
	// just dockerd via SSL_CERT_FILE) before handing off to the dind entrypoint.
	// Transparent egress additionally repoints the default route at the gvnet NIC
	// first. Then exec the dind entrypoint which prepares and runs the engine.
	prelude := config.CATrustSnippet
	if cfg.TransparentEgress {
		prelude = config.GvnetRouteSnippet + prelude
	}
	args = append(args, "--entrypoint", "/bin/sh", dockerImage, "-c",
		prelude+"exec dockerd-entrypoint.sh "+strings.Join(dockerd, " "))
	if out, err := runContainer(args...); err != nil {
		return fmt.Errorf("docker sidecar start failed: %s", out)
	}
	applyCPUPriority(&config.Config{ServerName: dockerName, CPUPriority: cfg.CPUPriority})
	if err := dockerAwait(cfg); err != nil {
		return err
	}
	return convertDockerMemory(cfg)
}

// ensureDockerVolume makes sure the sidecar's image-store volume exists AND is
// usable. A plain "volume inspect || create" is not enough: if the volume's
// backing image is removed out from under the runtime (e.g. a host disk
// cleanup), the record survives as a dangling entry — `volume inspect` still
// succeeds, but the sidecar can't mount the missing image and bootstrap fails
// with "Operation not supported". Detect that and heal by recreating the
// volume, so k3c recovers automatically instead of failing every `docker up`.
func ensureDockerVolume(name string) error {
	out, err := runContainer("volume", "inspect", name)
	if err != nil {
		logger.Info("creating docker image store volume (" + name + ")")
		if out, err := runContainer("volume", "create", name); err != nil {
			return fmt.Errorf("creating volume: %s", out)
		}
		return nil
	}

	// The record exists — verify its backing image is still on disk.
	var vols []struct {
		Configuration struct {
			Source string `json:"source"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal([]byte(out), &vols); err != nil || len(vols) == 0 {
		return nil // can't introspect; assume usable
	}
	src := vols[0].Configuration.Source
	if src == "" {
		return nil
	}
	if _, err := os.Stat(src); err == nil {
		return nil // backing present — healthy
	}

	// Dangling: backing is gone. The runtime's `volume delete` errors when the
	// backing entity is missing, so recreate a stub at the source path first so
	// the stale record can be deleted, then create a fresh, valid volume.
	logger.Warn("docker image store volume '" + name + "' is dangling (backing " + src + " missing) — recreating")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		return fmt.Errorf("recreating volume backing dir: %w", err)
	}
	if f, err := os.OpenFile(src, os.O_CREATE, 0o644); err == nil { //nolint:gosec // path from the runtime
		_ = f.Close()
	}
	if out, err := runContainer("volume", "delete", name); err != nil {
		return fmt.Errorf("removing dangling volume: %s", out)
	}
	logger.Info("creating docker image store volume (" + name + ")")
	if out, err := runContainer("volume", "create", name); err != nil {
		return fmt.Errorf("recreating volume: %s", out)
	}
	return nil
}

const (
	buildkitBaseImage  = "moby/buildkit:buildx-stable-1"
	buildkitLocalImage = "k3c-buildkit:latest"
	dockerContextName  = "k3c"
)

// DockerBuildkit creates (or recreates) a buildx "docker-container" builder in
// the sidecar that works under k3c: it trusts the cluster CA (the sidecar's
// egress is TLS-intercepted, so BuildKit otherwise rejects every registry cert
// as "unknown authority") and routes through the k3c proxy (the sidecar has no
// direct DNS). Without this, `docker buildx` builds fail even though plain
// `docker build`/pull work — because BuildKit runs in its own container that,
// unlike dockerd, k3c doesn't configure. This is what lets buildx-based image
// builds work the way they did on Docker Desktop / OrbStack.
//
// It is corporate-CA-agnostic: it bakes whatever caCerts resolve to (possibly
// none) into the BuildKit image.
func DockerBuildkit(cfg *config.Config, name string) error {
	if !containerExists(dockerName, true) {
		return fmt.Errorf("docker sidecar is not running — start it with: k3c docker up")
	}
	if name == "" {
		name = "multi-platform"
	}

	image := buildkitBaseImage
	ca, err := corpCACerts(cfg)
	if err != nil {
		return err
	}
	if len(ca) > 0 {
		if err := buildBuildkitImage(ca); err != nil {
			return err
		}
		image = buildkitLocalImage
	}

	_ = dockerCmd("buildx", "rm", name).Run() // ignore: may not exist
	args := []string{"buildx", "create", "--name", name, "--driver", "docker-container",
		"--driver-opt", "image=" + image}
	if !cfg.TransparentEgress {
		// Proxy mode: BuildKit has no DNS, so route registry pulls through the
		// k3c proxy. With transparent egress the sidecar has real DNS + egress,
		// so no proxy is needed (and direct is more robust).
		proxy := "http://" + cfg.VmnetGateway + ":" + cfg.ProxyPort
		args = append(args,
			"--driver-opt", "env.HTTP_PROXY="+proxy,
			"--driver-opt", "env.HTTPS_PROXY="+proxy,
			// single comma-free value: --driver-opt splits on commas. The vmnet
			// /24 covers the in-cluster registry so it skips the proxy.
			"--driver-opt", "env.NO_PROXY="+vmnetCIDR(cfg.VmnetGateway))
	}
	args = append(args, "--bootstrap")
	create := dockerCmd(args...)
	create.Stdout, create.Stderr = os.Stdout, os.Stderr
	if err := create.Run(); err != nil {
		return fmt.Errorf("creating buildx builder %q: %w", name, err)
	}
	logger.Info("buildx builder '" + name + "' is ready (trusts the cluster CA, egress via the k3c proxy)")
	return nil
}

// buildBuildkitImage bakes the corporate CA bundle into a BuildKit image so the
// builder trusts the proxy's re-signed certificates.
func buildBuildkitImage(ca []byte) error {
	dir, err := os.MkdirTemp("", "k3c-buildkit")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "corp-ca.crt"), ca, 0o644); err != nil {
		return err
	}
	dockerfile := "FROM " + buildkitBaseImage + "\n" +
		"COPY corp-ca.crt /tmp/corp-ca.crt\n" +
		"RUN cat /tmp/corp-ca.crt >> /etc/ssl/certs/ca-certificates.crt\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return err
	}
	logger.Info("building CA-trusting buildkit image " + buildkitLocalImage)
	// Pin the sidecar's native arch: the buildkit daemon runs natively on the
	// sidecar, so this image must match it. Without --platform the build inherits
	// any DOCKER_DEFAULT_PLATFORM from the environment (e.g. linux/amd64 exported
	// for cross-arch app builds), which produces an emulated image whose RUN step
	// crashes under Rosetta ("unable to mmap ELF"). --provenance=false keeps it a
	// plain single-arch image (matches bakeNodeImage).
	build := dockerCmd("build", "--platform", "linux/"+sidecarArch(), "--provenance=false",
		"-t", buildkitLocalImage, dir)
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("building buildkit image: %w", err)
	}
	return nil
}

// dockerCmd runs the docker CLI against the k3c sidecar context.
func dockerCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("docker", args...) //nolint:gosec // args are k3c-controlled
	cmd.Env = append(os.Environ(), "DOCKER_CONTEXT="+dockerContextName)
	return cmd
}

// vmnetCIDR turns a gateway IP (e.g. 192.168.64.1) into its /24 (192.168.64.0/24).
func vmnetCIDR(gateway string) string {
	if i := strings.LastIndex(gateway, "."); i > 0 {
		return gateway[:i] + ".0/24"
	}
	return gateway
}

// forwardRegistryLoopback makes 127.0.0.1:<RegistryPort> inside the sidecar
// reach the cluster's local registry. The registry lives off-VM (reachable at
// the vmnet gateway), but build tooling tags images localhost:<port> — the
// same address the host and the k3s node resolve to the registry — so the
// sidecar's engine must resolve it too, or `docker push localhost:<port>/…`
// fails with "connection refused". Loopback isn't routed off-host by default,
// so enable route_localnet and DNAT it to the gateway, MASQUERADE'ing the
// source so replies find their way back. Best-effort and idempotent.
func forwardRegistryLoopback(cfg *config.Config) {
	if !cfg.RegistryEnabled || cfg.RegistryPort == "" {
		return
	}
	p, gw := cfg.RegistryPort, cfg.VmnetGateway
	script := strings.Join([]string{
		"sysctl -w net.ipv4.conf.all.route_localnet=1 >/dev/null 2>&1 || true",
		fmt.Sprintf("iptables -t nat -C OUTPUT -p tcp -d 127.0.0.1 --dport %s -j DNAT --to-destination %s:%s 2>/dev/null || "+
			"iptables -t nat -A OUTPUT -p tcp -d 127.0.0.1 --dport %s -j DNAT --to-destination %s:%s", p, gw, p, p, gw, p),
		fmt.Sprintf("iptables -t nat -C POSTROUTING -p tcp -d %s --dport %s -j MASQUERADE 2>/dev/null || "+
			"iptables -t nat -A POSTROUTING -p tcp -d %s --dport %s -j MASQUERADE", gw, p, gw, p),
	}, "; ")
	if out, err := runContainer("exec", dockerName, "sh", "-c", script); err != nil {
		logger.Warn("registry loopback forward (push to localhost:" + p + " may fail): " + out)
	}
}

// ensureDockerForwarder injects and (re)launches the in-guest nested-port
// forwarder. The forwarder binary is staged into the sidecar via the mounted
// /k3c-ca dir and run detached, listening on the unix socket that
// --publish-socket bridges to dockerForwardSocketPath on the host (so the host
// reaches nested published ports without the guest vmnet IP). Best-effort: if
// the forwarder binary isn't shipped (e.g. an unbundled dev build), nested
// published ports simply aren't forwarded. Re-run on every `up` — exec'd
// processes die when the VM stops.
func ensureDockerForwarder(cfg *config.Config) {
	src := runtime.DockerForwarderBinary()
	if src == "" {
		logger.Warn("docker: in-guest forwarder binary not found, so nested published ports " +
			"(e.g. Testcontainers mapped ports) won't be reachable from the host. Build it next " +
			"to k3c (make build / make docker-fwd) or set K3C_DOCKER_FWD_BINARY.")
		return
	}
	// The forwarder only helps if the runtime actually bridges its socket to the
	// host — which happens only for sidecars created WITH --publish-socket. A
	// sidecar created before this feature has no bridge; surface that with the
	// fix rather than silently not forwarding nested ports.
	if _, err := os.Stat(dockerForwardSocketPath(cfg)); err != nil {
		logger.Warn("docker: this sidecar predates the nested-port bridge, so published container " +
			"ports (e.g. Testcontainers mapped ports) are not reachable from the host. Recreate it " +
			"to enable them: k3c docker rm && k3c docker up")
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		logger.Warn("docker: reading in-guest forwarder: " + err.Error())
		return
	}
	// stage it where the sidecar already mounts host files (-v certDir:/k3c-ca)
	dst := filepath.Join(cfg.BaseDir, "docker", "k3c-docker-fwd")
	if err := writeFileAtomic(dst, data, 0o755); err != nil {
		logger.Warn("docker: staging in-guest forwarder: " + err.Error())
		return
	}
	// kill any prior instance, then exec the forwarder detached in the VM
	script := "kill $(pidof k3c-docker-fwd) 2>/dev/null; exec /k3c-ca/k3c-docker-fwd -socket " + guestForwardSocket
	if out, err := runContainer("exec", "-d", dockerName, "sh", "-c", script); err != nil {
		logger.Warn("docker: launching in-guest forwarder: " + out)
	}
}

// applyDockerSysctls raises the sidecar VM's kernel limits (the configured
// node sysctls — notably the inotify instance/watch limits, whose defaults are
// far too low for file-watching workloads) so nested k3d pods get the same
// limits the native cluster sets on its node. Apple `container run` has no
// --sysctl, so they are set via exec once the VM is up; best-effort.
func applyDockerSysctls(cfg *config.Config) {
	if len(cfg.Sysctls) == 0 {
		return
	}
	keys := make([]string, 0, len(cfg.Sysctls))
	for k := range cfg.Sysctls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := []string{"exec", dockerName, "sysctl", "-w"}
	for _, k := range keys {
		args = append(args, k+"="+cfg.Sysctls[k])
	}
	if out, err := runContainer(args...); err != nil {
		logger.Warn("setting docker sidecar sysctls: " + out)
	}
}

// dockerAwait waits until the engine answers, then finalizes the sidecar.
func dockerAwait(cfg *config.Config) error {
	// a restored sidecar's virtiofs share (/k3c-ca) can come back dead;
	// every start path funnels through here, so heal it before the engine
	// is used (dead share = dockerd cannot read the corporate CA bundle)
	repairDockerVirtiofs()
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
	// raise the sidecar VM kernel limits (notably inotify) so nested k3d pods
	// don't hit the low defaults the native cluster also overrides
	applyDockerSysctls(cfg)
	// make localhost:<registry-port> reach the local registry from the sidecar,
	// so `docker push localhost:<port>/…` (build tooling tags images this way,
	// the same address the host and node resolve) works from the sidecar engine
	forwardRegistryLoopback(cfg)
	// launch the in-guest forwarder so nested published container ports are
	// reachable from the host (without the guest vmnet IP)
	ensureDockerForwarder(cfg)
	// nested-k3d node images are NOT prepared here — starting the engine
	// shouldn't pay that one-time pull/bake. Run `k3c docker prepare-k3d`
	// once before using `k3d cluster create` (see PrepareK3sNodeImages).
	if ensureDockerContext(cfg, host) {
		logger.Info("docker context '" + cfg.DockerContext + "' active")
		warnContextShadowed(cfg)
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

// warnContextShadowed warns when a DOCKER_HOST or DOCKER_CONTEXT in the
// environment overrides the context we just activated. The docker CLI resolves
// the engine in the order DOCKER_HOST, then DOCKER_CONTEXT, then the active
// context — so either env var (e.g. the DOCKER_HOST OrbStack/Docker Desktop
// export into your shell) silently keeps `docker` pointed at the wrong engine
// even though `docker context use` succeeded. `docker context use` prints the
// same warning to stderr, but we capture its output, so surface it ourselves.
func warnContextShadowed(cfg *config.Config) {
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		logger.Warn("DOCKER_HOST=" + h + " overrides the '" + cfg.DockerContext +
			"' context, so docker still targets that engine. Run 'unset DOCKER_HOST' to use the sidecar.")
		return
	}
	if c := os.Getenv("DOCKER_CONTEXT"); c != "" && c != cfg.DockerContext {
		logger.Warn("DOCKER_CONTEXT=" + c + " overrides the '" + cfg.DockerContext +
			"' context, so docker still targets that engine. Run 'unset DOCKER_CONTEXT' to use the sidecar.")
	}
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

// DockerHost returns the engine endpoint: the daemon's host unix socket,
// which forwards to the sidecar engine. A stable path (it survives sidecar
// recreation, unlike the VM IP) that the docker context and DOCKER_HOST point
// at. Published container ports are mirrored to the host loopback by the
// daemon (startDockerPortForward), so tools that connect to mapped ports on
// localhost (the convention for a unix-socket DOCKER_HOST) reach them.
func DockerHost(cfg *config.Config) (string, error) {
	if !containerExists(dockerName, true) {
		return "", fmt.Errorf("docker sidecar is not running (k3c docker up)")
	}
	return "unix://" + dockerSocketPath(cfg), nil
}

// DockerHostTCP returns the engine's tcp endpoint for tools that cannot use a
// unix socket. It is the stable Apple-published loopback (127.0.0.1:<DockerPort>),
// not the guest vmnet IP — the latter is not host-reachable (see
// startDockerSocket / the docker-sidecar-host-forwarder change).
func DockerHostTCP(cfg *config.Config) (string, error) {
	if !containerExists(dockerName, true) {
		return "", fmt.Errorf("docker sidecar is not running (k3c docker up)")
	}
	return "tcp://" + dockerEngineEndpoint(cfg), nil
}

// DockerEnv prints shell exports for the sidecar engine.
//
// DOCKER_HOST is the host unix socket. Testcontainers resolves the address it
// connects mapped ports on from DOCKER_HOST: a unix socket (or npipe) means
// "localhost", a tcp/ssh URL means that URL's host. Because the daemon mirrors
// every published container port onto host loopback (startDockerPortForward →
// the in-guest forwarder), "localhost" reaches them — so TESTCONTAINERS_HOST_-
// OVERRIDE is deliberately NOT set: loopback surfacing makes it unnecessary, and
// the guest vmnet IP it would otherwise name is not host-reachable on macOS 26
// (see the docker-sidecar-host-forwarder change).
//
// We do NOT force TESTCONTAINERS_RYUK_DISABLED. Ryuk (the reaper) is the one
// container Testcontainers pins to the engine's default `bridge` network, which
// can intermittently fail to start on VM-backed engines (as documented for
// colima/podman). Leaving it unset means `eval $(k3c docker env)` won't clobber a
// consumer's `TESTCONTAINERS_RYUK_DISABLED=true` workaround; mapped ports still
// surface on loopback either way (the test cleans up via t.Cleanup when Ryuk is
// off).
func DockerEnv(cfg *config.Config) error {
	host, err := DockerHost(cfg)
	if err != nil {
		return err
	}
	fmt.Println("export DOCKER_HOST=" + host)
	return nil
}

// DockerDown stops the sidecar (the image store volume stays) and restores
// the default docker context.
func DockerDown(cfg *config.Config) error {
	if !containerExists(dockerName, false) {
		return fmt.Errorf("docker sidecar does not exist")
	}
	dockerResumeIfPaused(cfg) // a frozen VM cannot be stopped cleanly
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

// DockerRemove deletes the sidecar container so a subsequent `up` recreates it
// (e.g. to change CPU/memory). The image-store volume is kept unless
// removeVolume is set; the gvnet netstack/network and docker context are torn
// down either way.
func DockerRemove(cfg *config.Config, removeVolume bool) error {
	existed := containerExists(dockerName, false)
	if existed {
		dockerResumeIfPaused(cfg) // clears the paused marker too
		logger.Info("removing docker sidecar")
		if out, err := runContainer("rm", "-f", dockerName); err != nil {
			return fmt.Errorf("removing docker sidecar: %s", out)
		}
	}
	if cfg.TransparentEgress {
		removeGvnet(cfg, dockerName)
	}
	restoreDockerContext(cfg)
	if removeVolume {
		logger.Info("removing docker image store volume (" + dockerVolume + ")")
		if out, err := runContainer("volume", "rm", dockerVolume); err != nil {
			logger.Warn("removing volume " + dockerVolume + ": " + out)
		}
	}
	switch {
	case !existed && !removeVolume:
		return fmt.Errorf("docker sidecar does not exist")
	case removeVolume:
		logger.Info("docker sidecar and image store removed")
	default:
		logger.Info("docker sidecar removed (image store kept; k3c docker up recreates it)")
	}
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
				if h := os.Getenv("DOCKER_HOST"); h != "" {
					active += ", shadowed by DOCKER_HOST=" + h
				} else if c := os.Getenv("DOCKER_CONTEXT"); c != "" && c != name {
					active += ", shadowed by DOCKER_CONTEXT=" + c
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
