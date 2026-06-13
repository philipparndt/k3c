// Package cluster manages k3s clusters on Apple `container`.
package cluster

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
	"k3c/runtime"
)

// SetContainerBinary records the Apple `container` CLI from the k3c config.
// The runtime package decides whether to honour it (explicit user config) or
// fall back to the bundled/PATH runtime — see package runtime for the full
// resolution precedence.
func SetContainerBinary(path string) {
	runtime.SetConfiguredBinary(path)
}

func containerBinary() string {
	return runtime.Binary()
}

// runContainer executes the resolved container CLI with its required
// environment (e.g. CONTAINER_INSTALL_ROOT for the bundled runtime) and
// returns the trimmed combined output.
func runContainer(args ...string) (string, error) {
	return runtime.Output(args...)
}

// capabilities of the container CLI, probed once from its help output.
type containerCapabilities struct {
	pause   bool
	suspend bool
	memory  bool
}

var (
	capsOnce sync.Once
	caps     containerCapabilities
)

func capabilities() containerCapabilities {
	capsOnce.Do(func() {
		out, _ := runContainer("--help")
		caps.pause = strings.Contains(out, "\n  pause") && strings.Contains(out, "\n  resume")
		caps.suspend = strings.Contains(out, "\n  suspend")
		caps.memory = strings.Contains(out, "\n  memory")
	})
	return caps
}

// runOut executes a command and returns its combined output.
func runOut(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func preflight() error {
	// kubectl is always required from PATH. `container` is only required
	// from PATH when no bundled runtime is embedded (the bundled runtime
	// provides its own binary).
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl is not installed")
	}
	if !runtime.HasBundle() && os.Getenv("K3C_CONTAINER_BINARY") == "" {
		if _, err := exec.LookPath(containerBinary()); err != nil {
			return fmt.Errorf("container is not installed")
		}
	}
	out, err := runContainer("--version")
	if err != nil {
		return fmt.Errorf("container CLI not working: %s", out)
	}
	if strings.Contains(out, "version 0.") {
		return fmt.Errorf("container CLI >= 1.0.0 required")
	}
	// EnsureSystem extracts the bundled runtime (if any), starts the system
	// services, and loads the bundled init image when missing.
	if err := runtime.EnsureSystem(); err != nil {
		return err
	}
	return nil
}

// containerExists reports whether a container with this exact name exists
// (running when runningOnly is set).
func containerExists(name string, runningOnly bool) bool {
	args := []string{"ls", "--format", "json"}
	if !runningOnly {
		args = []string{"ls", "-a", "--format", "json"}
	}
	out, _ := runContainer(args...)
	return strings.Contains(out, `"`+name+`"`)
}

// otherRunningServer returns the name of a foreign <cluster>-server
// container that is currently running, if any.
func otherRunningServer(cfg *config.Config) string {
	out, _ := runContainer("ls")
	for _, line := range strings.Split(out, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasSuffix(fields[0], "-server") && fields[0] != cfg.ServerName {
			return fields[0]
		}
	}
	return ""
}

func startRegistry(cfg *config.Config) error {
	if !cfg.RegistryEnabled {
		return nil
	}
	if containerExists(cfg.RegistryName, true) {
		logger.Info("local registry already running")
		return nil
	}
	_, _ = runContainer("rm", "-f", cfg.RegistryName)
	logger.Info("starting local registry on port " + cfg.RegistryPort)
	out, err := runContainer("run", "-d",
		"--name", cfg.RegistryName,
		"-p", "127.0.0.1:"+cfg.RegistryPortInternal+":5000",
		"docker.io/registry:2")
	if err != nil {
		return fmt.Errorf("registry start failed: %s", out)
	}
	return nil
}

// prepareNodeConfig writes the bind-mounted /etc/rancher/k3s content: the
// registries.yaml from the config, and a CA bundle of the host's system
// roots plus any configured certificates (ca-bundle.pem, which the
// registries config may reference).
func prepareNodeConfig(cfg *config.Config) error {
	logger.Info("preparing node config (registries.yaml, CA bundle)")
	etc := cfg.K3sEtcDir()
	if err := os.MkdirAll(etc, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ImagesDir(), 0o755); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(etc, "k3s.yaml"))
	if registries := cfg.EffectiveRegistries(); registries != "" {
		if err := os.WriteFile(filepath.Join(etc, "registries.yaml"), []byte(registries), 0o644); err != nil {
			return err
		}
	}
	bundle, err := caBundle(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(etc, "ca-bundle.pem"), bundle, 0o644)
}

// caBundle builds the trust bundle for guests: the host's system roots
// plus the configured corporate CAs.
func caBundle(cfg *config.Config) ([]byte, error) {
	bundle, err := os.ReadFile("/etc/ssl/cert.pem")
	if err != nil {
		return nil, fmt.Errorf("reading system CA bundle: %w", err)
	}
	for _, glob := range cfg.CACertGlobs {
		matches, err := filepath.Glob(glob)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no CA certificates match %s", glob)
		}
		for _, crt := range matches {
			data, err := os.ReadFile(crt)
			if err != nil {
				return nil, err
			}
			bundle = append(bundle, '\n')
			bundle = append(bundle, data...)
		}
	}
	return bundle, nil
}

func startServer(cfg *config.Config) error {
	logger.Info(fmt.Sprintf("starting k3s server (%s cpus, %s memory)", cfg.CPUs, cfg.Memory))
	modernKernel := KernelHasModernNetfilter()
	if modernKernel {
		logger.Info("kernel has br_netfilter + vxlan: flannel default (vxlan), no masquerade workaround")
	} else {
		logger.Info("kernel lacks br_netfilter/vxlan: applying host-gw + masquerade-all workarounds")
	}
	args := []string{"run", "-d",
		"--name", cfg.ServerName,
		// k3s remounts /sys, mounts cgroups, etc. — needs full capabilities
		"--cap-add", "ALL",
		// amd64-only images run via Rosetta binfmt, matching Docker Desktop
		"--rosetta",
		"-m", cfg.Memory,
		"-c", cfg.CPUs,
		"--tmpfs", "/run",
		"--tmpfs", "/var/run",
		"-v", cfg.K3sEtcDir() + ":/etc/rancher/k3s",
		// k3s watches this directory and imports image tarballs dropped
		// into it (used by `k3c image import`)
		"-v", cfg.ImagesDir() + ":/var/lib/rancher/k3s/agent/images",
	}
	if cfg.TransparentEgress {
		// dual-NIC: gvnet NIC (default route, transparent egress) + the vmnet
		// default network (host<->VM, published ports); no CONNECT proxy needed
		nets, err := gvnetNetworks(cfg, cfg.ServerName)
		if err != nil {
			return err
		}
		args = append(args, nets...)
	} else {
		proxyURL := fmt.Sprintf("http://%s:%s", cfg.VmnetGateway, cfg.ProxyPort)
		args = append(args,
			"-e", "HTTP_PROXY="+proxyURL,
			"-e", "HTTPS_PROXY="+proxyURL,
			"-e", "NO_PROXY="+cfg.NoProxy())
	}
	args = append(args,
		"-p", "0.0.0.0:"+cfg.APIPortInternal+":6443",
		"-p", "127.0.0.1:"+cfg.IngressPortInternal+":443",
		"--entrypoint", "/bin/sh",
		cfg.Image,
		"-c", cfg.K3sCommand(modernKernel))
	if out, err := runContainer(args...); err != nil {
		return fmt.Errorf("k3s start failed: %s", out)
	}
	return nil
}

// startServerVM (re)starts the existing server VM, first ensuring its
// transparent-egress netstack is running (the per-VM netstack exits when its
// VM disconnects, so a restart needs a fresh one). Returns the command output.
func startServerVM(cfg *config.Config) (string, error) {
	if cfg.TransparentEgress {
		if _, err := ensureGvnet(cfg, cfg.ServerName); err != nil {
			return "", err
		}
	}
	return runContainer("start", cfg.ServerName)
}

// kubeconfig reads the kubeconfig k3s wrote into the bind mount
// (container exec/cp hang once k3s runs, hence the mount), renames the
// identifiers, and points the server at the published port. With wait set
// it polls for up to two minutes (cluster creation).
func kubeconfig(cfg *config.Config, wait bool) (string, error) {
	path := filepath.Join(cfg.K3sEtcDir(), "k3s.yaml")
	attempts := 1
	if wait {
		attempts = 60
	}
	var raw []byte
	for i := 0; i < attempts; i++ {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			raw = data
			break
		}
		if wait {
			time.Sleep(2 * time.Second)
		}
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("no kubeconfig at %s; check: container logs %s", path, cfg.ServerName)
	}
	kc := string(raw)
	kc = strings.ReplaceAll(kc, ": default", ": "+cfg.KubeContext)
	kc = strings.ReplaceAll(kc,
		"server: https://127.0.0.1:6443",
		"server: https://"+cfg.APIHost+":"+cfg.APIPortInternal)
	return kc, nil
}

// KubeconfigGet prints the cluster's kubeconfig to stdout.
func KubeconfigGet(cfg *config.Config) error {
	// without the per-cluster ports the server URL gets an empty port
	_ = loadPorts(cfg)
	kc, err := kubeconfig(cfg, false)
	if err != nil {
		return err
	}
	fmt.Print(kc)
	return nil
}

// KubeconfigMerge merges the cluster's kubeconfig into ~/.kube/config and
// switches the current context.
func KubeconfigMerge(cfg *config.Config) error {
	// without the per-cluster ports the server URL gets an empty port,
	// which is not even valid YAML
	_ = loadPorts(cfg)
	logger.Info("waiting for kubeconfig")
	kc, err := kubeconfig(cfg, true)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "k3c-kubeconfig-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(kc); err != nil {
		return err
	}
	tmp.Close()

	logger.Info("merging kubeconfig (context: " + cfg.KubeContext + ")")
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	kubeDir := filepath.Join(home, ".kube")
	kubeConfig := filepath.Join(kubeDir, "config")
	if err := os.MkdirAll(kubeDir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(kubeConfig); err == nil {
		if data, err := os.ReadFile(kubeConfig); err == nil {
			_ = os.MkdirAll(cfg.RunDir(), 0o755)
			_ = os.WriteFile(filepath.Join(cfg.RunDir(), "kubeconfig.backup"), data, 0o600)
		}
		merge := exec.Command("kubectl", "config", "view", "--flatten")
		merge.Env = append(os.Environ(), "KUBECONFIG="+tmp.Name()+":"+kubeConfig)
		merged, err := merge.Output()
		if err != nil {
			detail := ""
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				detail = ": " + strings.TrimSpace(string(exitErr.Stderr))
			}
			return fmt.Errorf("kubeconfig merge failed: %w%s", err, detail)
		}
		if err := os.WriteFile(kubeConfig, merged, 0o600); err != nil {
			return err
		}
	} else {
		if err := os.WriteFile(kubeConfig, []byte(kc), 0o600); err != nil {
			return err
		}
	}
	_, err = runOut("kubectl", "config", "use-context", cfg.KubeContext)
	return err
}

func kubectl(cfg *config.Config, args ...string) (string, error) {
	return runOut("kubectl", append([]string{"--context", cfg.KubeContext}, args...)...)
}

func kubectlCommand(cfg *config.Config, args ...string) *exec.Cmd {
	return exec.Command("kubectl", append([]string{"--context", cfg.KubeContext}, args...)...)
}

func waitReady(cfg *config.Config) error {
	logger.Info("waiting for node to become Ready")
	for i := 0; i < 60; i++ {
		out, err := kubectl(cfg, "get", "nodes")
		if err == nil && strings.Contains(out, " Ready") {
			fmt.Println(out)
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("node did not become Ready; check: container logs %s", cfg.ServerName)
}

// setupCoreDNS applies the egress DNS override, if egress domains are
// configured. The coredns deployment is created asynchronously by the k3s
// addon controller, so wait for it first.
func setupCoreDNS(cfg *config.Config) error {
	manifest := cfg.CorednsCustom()
	if manifest == "" {
		return nil
	}
	logger.Info("configuring CoreDNS egress override")
	for i := 0; i < 60; i++ {
		if _, err := kubectl(cfg, "-n", "kube-system", "get", "deployment", "coredns"); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	apply := exec.Command("kubectl", "--context", cfg.KubeContext, "apply", "-f", "-")
	apply.Stdin = strings.NewReader(manifest)
	if out, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("coredns-custom apply failed: %s", out)
	}
	if out, err := kubectl(cfg, "-n", "kube-system", "rollout", "restart", "deployment", "coredns"); err != nil {
		return fmt.Errorf("coredns restart failed: %s", out)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		out, err := kubectl(cfg, "-n", "kube-system", "rollout", "status", "deployment", "coredns", "--timeout=15s")
		if err == nil {
			return nil
		}
		// waiting any longer is pointless when the server is gone
		if !containerExists(cfg.ServerName, true) {
			return errServerExited
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("coredns rollout: %s", out)
		}
	}
}

// errServerExited reports that the server container stopped while the
// cluster was being configured (e.g. a stop/start lifecycle race right
// after a restore); the caller restarts it once.
var errServerExited = fmt.Errorf("the server container exited unexpectedly")

// Create creates and starts a new cluster.
func Create(cfg *config.Config) error {
	if err := preflight(); err != nil {
		return err
	}
	// upgrade an old kernel before the node VM is created so the new cluster
	// boots on a br_netfilter/vxlan-capable kernel and skips the workarounds
	EnsureRecommendedKernel()
	if containerExists(cfg.ServerName, false) {
		return fmt.Errorf("cluster '%s' already exists; run delete (or start) first", cfg.Cluster)
	}
	if err := ensurePorts(cfg); err != nil {
		return err
	}
	if cfg.ConfigFile == "" {
		logger.Warn("no project config (k3c.yaml) found — creating '" + cfg.Cluster + "' with generic defaults; run from the project directory or pass --config if that is not intended")
	}
	if err := SpawnDaemons(cfg); err != nil {
		return err
	}
	if err := startRegistry(cfg); err != nil {
		return err
	}
	if err := prepareNodeConfig(cfg); err != nil {
		return err
	}
	if err := persistProjectConfig(cfg); err != nil {
		return err
	}
	if err := startServer(cfg); err != nil {
		return err
	}
	applyCPUPriority(cfg)
	if err := KubeconfigMerge(cfg); err != nil {
		return err
	}
	if err := waitReady(cfg); err != nil {
		return err
	}
	applyControlPlaneWeight(cfg)
	// register the webhook before the coredns setup: a failed rollout must
	// not leave a cluster that silently admits pods with full requests
	if err := applyIgnoreCPUWebhook(cfg); err != nil {
		return err
	}
	if err := setupCoreDNS(cfg); err != nil {
		return err
	}
	if err := setActive(cfg); err != nil {
		return err
	}
	logger.Info("cluster is up (context: " + cfg.KubeContext + ")")
	return nil
}

// persistProjectConfig copies the project config into the cluster state
// dir, so start/stop/kubeconfig resolve it from any working directory.
func persistProjectConfig(cfg *config.Config) error {
	persisted := filepath.Join(cfg.RunDir(), "k3c.yaml")
	if cfg.ConfigFile == "" || cfg.ConfigFile == persisted {
		return nil
	}
	data, err := os.ReadFile(cfg.ConfigFile)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(persisted); err == nil && string(old) == string(data) {
		return nil
	}
	logger.Info("updating the cluster's persisted project config from " + cfg.ConfigFile)
	return os.WriteFile(persisted, data, 0o644)
}

// Delete removes a cluster, its state, and its kubeconfig entries.
func Delete(cfg *config.Config, snapshots bool) error {
	resumeIfPaused(cfg)
	logger.Info("removing containers")
	_, _ = runContainer("rm", "-f", cfg.ServerName)
	_, _ = runContainer("rm", "-f", cfg.RegistryName)
	if cfg.TransparentEgress {
		removeGvnet(cfg, cfg.ServerName)
	}
	StopDaemons(cfg)
	for _, kind := range []string{"delete-context", "delete-cluster", "delete-user"} {
		_, _ = runOut("kubectl", "config", kind, cfg.KubeContext)
	}
	_ = os.RemoveAll(cfg.RunDir())
	if snapshots {
		logger.Info("removing snapshots")
		_ = os.RemoveAll(filepath.Join(cfg.BaseDir, "snapshots", cfg.Cluster))
	}
	logger.Info("deleted")
	return nil
}

// Stop stops a cluster's containers, preserving all state.
func Stop(cfg *config.Config) error {
	resumeIfPaused(cfg)
	logger.Info("stopping cluster '" + cfg.Cluster + "' (state is preserved)")
	_, _ = runContainer("stop", cfg.ServerName)
	_, _ = runContainer("stop", cfg.RegistryName)
	if cfg.TransparentEgress {
		stopGvnet(cfg, cfg.ServerName)
	}
	logger.Info("stopped; resume with: k3c cluster start " + cfg.Cluster)
	return nil
}

// Start resumes a stopped cluster.
func Start(cfg *config.Config) error {
	if err := preflight(); err != nil {
		return err
	}
	// refresh the persisted project config so edits to the project's
	// k3c.yaml take effect on a restart, also from other directories
	if err := persistProjectConfig(cfg); err != nil {
		return err
	}
	_ = loadPorts(cfg)
	if err := SpawnDaemons(cfg); err != nil {
		return err
	}
	_, _ = runContainer("start", cfg.RegistryName)
	if !containerExists(cfg.ServerName, true) {
		if out, err := startServerVM(cfg); err != nil {
			return fmt.Errorf("start failed: %s", out)
		}
	}
	_, _ = runOut("kubectl", "config", "use-context", cfg.KubeContext)
	applyCPUPriority(cfg)
	for attempt := 0; ; attempt++ {
		err := postStart(cfg)
		if err == nil {
			break
		}
		// A stop/start in quick succession (snapshot restore) can race the
		// runtime's lifecycle cleanup, which then stops the freshly started
		// server. Detect the death and start it again, once.
		if attempt == 0 && !containerExists(cfg.ServerName, true) {
			logger.Warn("server exited unexpectedly during startup; starting it again")
			if out, err := startServerVM(cfg); err != nil {
				return fmt.Errorf("restart failed: %s", out)
			}
			// the restart spawned a new VM process; without re-clamping it
			// runs the k3s boot storm at full priority
			applyCPUPriority(cfg)
			continue
		}
		return err
	}
	if err := setActive(cfg); err != nil {
		return err
	}
	logger.Info("cluster '" + cfg.Cluster + "' resumed (context: " + cfg.KubeContext + ")")
	return nil
}

// postStart brings a freshly started cluster into its configured state.
func postStart(cfg *config.Config) error {
	// virtiofs shares may come back dead from a restored machine state
	repairVirtiofs(cfg)
	if err := waitReady(cfg); err != nil {
		return err
	}
	applyControlPlaneWeight(cfg)
	// webhook first: a failed coredns rollout must not leave a cluster
	// that silently admits pods with full requests
	if err := applyIgnoreCPUWebhook(cfg); err != nil {
		return err
	}
	// re-apply so egress config changes take effect on a restart
	return setupCoreDNS(cfg)
}

// applyControlPlaneWeight raises the k3s cgroup's CPU weight inside the
// guest. The admission webhook strips pod CPU requests, so all pods get
// equal tiny weights — but the whole control plane (one k3s process:
// apiserver, datastore, scheduler) competes as a SIBLING cgroup at weight
// 100 against kubepods' 430 and starves during pod startup storms: the
// API stops answering and installs fail with handshake timeouts. Weight
// 1000 makes the control plane win contention; weights have no effect
// without contention.
func applyControlPlaneWeight(cfg *config.Config) {
	if out, err := runContainer("exec", cfg.ServerName,
		"sh", "-c", "echo 1000 > /sys/fs/cgroup/k3s/cpu.weight"); err != nil {
		logger.Debug("control plane cpu weight: " + strings.TrimSpace(out))
	}
}

// clusterStates maps cluster names to their server/registry container
// states.
func clusterStates() map[string]map[string]string {
	out, _ := runContainer("ls", "-a")
	state := map[string]map[string]string{}
	for _, line := range strings.Split(out, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		name, st := fields[0], fields[4]
		for _, suffix := range []string{"-server", "-registry"} {
			if strings.HasSuffix(name, suffix) {
				cluster := strings.TrimSuffix(name, suffix)
				if state[cluster] == nil {
					state[cluster] = map[string]string{}
				}
				state[cluster][suffix] = st
			}
		}
	}
	return state
}

// OnlyClusterName returns the name of the only existing cluster, or ""
// when there are none or several.
func OnlyClusterName() string {
	names := []string{}
	for cluster, parts := range clusterStates() {
		if parts["-server"] != "" {
			names = append(names, cluster)
		}
	}
	if len(names) == 1 {
		return names[0]
	}
	return ""
}

// Activate makes a cluster the current one: resumes or starts it as
// needed, points the public ingress/registry routing at it, and switches
// the kube context. Other running clusters are left untouched.
func Activate(cfg *config.Config) error {
	resumeIfPaused(cfg)
	return Start(cfg)
}

// List prints all k3c clusters (containers named <cluster>-server) with
// their server/registry state.
func List(cfg *config.Config) error {
	fmt.Printf("%-7s %-16s %-10s %-10s %-8s %s\n", "CURRENT", "NAME", "SERVER", "REGISTRY", "RAM", "CONTEXT")
	for _, c := range Clusters(cfg) {
		current := ""
		if c.Active {
			current = "*"
		}
		fmt.Printf("%-7s %-16s %-10s %-10s %-8s %s\n", current, c.Name, c.Server, c.Registry, c.RAM, c.Context)
	}
	return nil
}

// clusterRAM returns the OS-level physical memory footprint of the
// cluster's VM process (the Virtualization.framework process owns the
// guest memory; the supervisor's RSS is meaningless).
func clusterRAM(cluster string) string {
	pid := vzProcessPID(cluster + "-server")
	if pid == 0 {
		return "-"
	}
	out, err := runOut("footprint", strconv.Itoa(pid))
	if err != nil {
		return "-"
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "phys_footprint:") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				return fields[1] + fields[2]
			}
		}
	}
	return "-"
}

// Status prints daemon, container, and node state for a cluster.
func Status(cfg *config.Config) error {
	fmt.Println("--- host daemons ---")
	for name, pidFile := range map[string]string{"proxy": cfg.ProxyPidFile(), "sni-gateway": cfg.SNIPidFile()} {
		if pidAlive(pidFile) {
			fmt.Printf("%s: running\n", name)
		} else {
			fmt.Printf("%s: stopped\n", name)
		}
	}
	if isPaused(cfg) {
		fmt.Println("--- cluster is PAUSED (in memory; k3c cluster resume) ---")
	}
	fmt.Println("--- containers ---")
	out, _ := runContainer("ls", "-a")
	for i, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if i == 0 || (len(fields) > 0 && (fields[0] == cfg.ServerName || fields[0] == cfg.RegistryName)) {
			fmt.Println(line)
		}
	}
	fmt.Println("--- nodes ---")
	nodes, _ := kubectl(cfg, "get", "nodes")
	fmt.Println(nodes)
	return nil
}
