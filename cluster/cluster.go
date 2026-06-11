// Package cluster manages k3s clusters on Apple `container`.
package cluster

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	if cfg.Registries != "" {
		if err := os.WriteFile(filepath.Join(etc, "registries.yaml"), []byte(cfg.Registries), 0o644); err != nil {
			return err
		}
	}
	bundle, err := os.ReadFile("/etc/ssl/cert.pem")
	if err != nil {
		return fmt.Errorf("reading system CA bundle: %w", err)
	}
	for _, glob := range cfg.CACertGlobs {
		matches, err := filepath.Glob(glob)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			return fmt.Errorf("no CA certificates match %s", glob)
		}
		for _, crt := range matches {
			data, err := os.ReadFile(crt)
			if err != nil {
				return err
			}
			bundle = append(bundle, '\n')
			bundle = append(bundle, data...)
		}
	}
	return os.WriteFile(filepath.Join(etc, "ca-bundle.pem"), bundle, 0o644)
}

func startServer(cfg *config.Config) error {
	logger.Info(fmt.Sprintf("starting k3s server (%s cpus, %s memory)", cfg.CPUs, cfg.Memory))
	proxyURL := fmt.Sprintf("http://%s:%s", cfg.VmnetGateway, cfg.ProxyPort)
	out, err := runContainer("run", "-d",
		"--name", cfg.ServerName,
		// k3s remounts /sys, mounts cgroups, etc. — needs full capabilities
		"--cap-add", "ALL",
		// amd64-only images run via Rosetta binfmt, matching Docker Desktop
		"--rosetta",
		"-m", cfg.Memory,
		"-c", cfg.CPUs,
		"--tmpfs", "/run",
		"--tmpfs", "/var/run",
		"-v", cfg.K3sEtcDir()+":/etc/rancher/k3s",
		// k3s watches this directory and imports image tarballs dropped
		// into it (used by `k3c image import`)
		"-v", cfg.ImagesDir()+":/var/lib/rancher/k3s/agent/images",
		"-e", "HTTP_PROXY="+proxyURL,
		"-e", "HTTPS_PROXY="+proxyURL,
		"-e", "NO_PROXY="+cfg.NoProxy(),
		"-p", "0.0.0.0:"+cfg.APIPortInternal+":6443",
		"-p", "127.0.0.1:"+cfg.IngressPortInternal+":443",
		"--entrypoint", "/bin/sh",
		cfg.Image,
		"-c", cfg.K3sCommand())
	if err != nil {
		return fmt.Errorf("k3s start failed: %s", out)
	}
	return nil
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
			return fmt.Errorf("kubeconfig merge failed: %w", err)
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
	if out, err := kubectl(cfg, "-n", "kube-system", "rollout", "status", "deployment", "coredns", "--timeout=300s"); err != nil {
		return fmt.Errorf("coredns rollout: %s", out)
	}
	return nil
}

// Create creates and starts a new cluster.
func Create(cfg *config.Config) error {
	if err := preflight(); err != nil {
		return err
	}
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
	if err := KubeconfigMerge(cfg); err != nil {
		return err
	}
	if err := waitReady(cfg); err != nil {
		return err
	}
	if err := setupCoreDNS(cfg); err != nil {
		return err
	}
	if err := applyIgnoreCPUWebhook(cfg); err != nil {
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
	return os.WriteFile(persisted, data, 0o644)
}

// Delete removes a cluster, its state, and its kubeconfig entries.
func Delete(cfg *config.Config) error {
	resumeIfPaused(cfg)
	logger.Info("removing containers")
	_, _ = runContainer("rm", "-f", cfg.ServerName)
	_, _ = runContainer("rm", "-f", cfg.RegistryName)
	StopDaemons(cfg)
	for _, kind := range []string{"delete-context", "delete-cluster", "delete-user"} {
		_, _ = runOut("kubectl", "config", kind, cfg.KubeContext)
	}
	_ = os.RemoveAll(cfg.RunDir())
	logger.Info("deleted")
	return nil
}

// Stop stops a cluster's containers, preserving all state.
func Stop(cfg *config.Config) error {
	resumeIfPaused(cfg)
	logger.Info("stopping cluster '" + cfg.Cluster + "' (state is preserved)")
	_, _ = runContainer("stop", cfg.ServerName)
	_, _ = runContainer("stop", cfg.RegistryName)
	logger.Info("stopped; resume with: k3c cluster start " + cfg.Cluster)
	return nil
}

// Start resumes a stopped cluster.
func Start(cfg *config.Config) error {
	if err := preflight(); err != nil {
		return err
	}
	_ = loadPorts(cfg)
	if err := SpawnDaemons(cfg); err != nil {
		return err
	}
	_, _ = runContainer("start", cfg.RegistryName)
	if !containerExists(cfg.ServerName, true) {
		if out, err := runContainer("start", cfg.ServerName); err != nil {
			return fmt.Errorf("start failed: %s", out)
		}
	}
	_, _ = runOut("kubectl", "config", "use-context", cfg.KubeContext)
	if err := waitReady(cfg); err != nil {
		return err
	}
	if err := applyIgnoreCPUWebhook(cfg); err != nil {
		return err
	}
	if err := setActive(cfg); err != nil {
		return err
	}
	logger.Info("cluster '" + cfg.Cluster + "' resumed (context: " + cfg.KubeContext + ")")
	return nil
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
	state := clusterStates()
	active := readActive(cfg).Cluster
	names := make([]string, 0, len(state))
	for cluster := range state {
		names = append(names, cluster)
	}
	sort.Strings(names)
	fmt.Printf("%-7s %-16s %-10s %-10s %-8s %s\n", "CURRENT", "NAME", "SERVER", "REGISTRY", "RAM", "CONTEXT")
	for _, cluster := range names {
		parts := state[cluster]
		if parts["-server"] == "" {
			continue
		}
		server := parts["-server"]
		registry := parts["-registry"]
		if registry == "" {
			registry = "-"
		}
		// a paused cluster's containers still report "running"
		if _, err := os.Stat(filepath.Join(cfg.BaseDir, "clusters", cluster, "paused")); err == nil {
			server = "paused"
			if registry != "-" {
				registry = "paused"
			}
		}
		// resolve per cluster: picks up its persisted project config
		context := cfg.ContextPrefix() + cluster
		if clusterCfg, err := config.Resolve(cluster, ""); err == nil {
			context = clusterCfg.KubeContext
		}
		current := ""
		if cluster == active {
			current = "*"
		}
		fmt.Printf("%-7s %-16s %-10s %-10s %-8s %s\n", current, cluster, server, registry, clusterRAM(cluster), context)
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
