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
	"k3c/ui"
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
	pause        bool
	suspend      bool
	memory       bool
	memoryPolicy bool // runtime-managed continuous balloon sizing
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
		if caps.memory {
			out, _ := runContainer("memory", "--help")
			caps.memoryPolicy = strings.Contains(out, "policy")
		}
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

// caBundle builds the trust bundle for guests: the host's system roots, the
// CAs the host actually trusts (macOS System keychain), plus any configured
// corporate CAs.
func caBundle(cfg *config.Config) ([]byte, error) {
	bundle, err := os.ReadFile("/etc/ssl/cert.pem")
	if err != nil {
		return nil, fmt.Errorf("reading system CA bundle: %w", err)
	}
	// /etc/ssl/cert.pem carries only the built-in roots; a CA an admin added to
	// the macOS System keychain (e.g. a corporate root doing TLS interception,
	// or an internal registry CA) is trusted by the host but absent from that
	// file. Share whatever the host trusts so guests trust exactly the same set
	// — without depending on caCerts globs pointing at the right files. This
	// pins no specific CA; it re-shares the host's own trust store.
	if extra := systemKeychainCerts(); len(extra) > 0 {
		bundle = append(bundle, '\n')
		bundle = append(bundle, extra...)
	}
	for _, glob := range cfg.CACertGlobs {
		matches, err := filepath.Glob(glob)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			// A configured CA glob that matches nothing is normal (no corporate
			// CAs, or per-cluster certs not generated yet). The system bundle is
			// always included; corporate CAs are additive, so skip, don't fail.
			logger.Warn("no CA certificates match " + glob + " — skipping")
			continue
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

// systemKeychainCerts returns the certificates in the macOS System keychain as
// PEM (admin-installed roots like a corporate CA live there, not in
// /etc/ssl/cert.pem). It is best-effort: a failure (non-macOS host, no
// `security` tool) yields nil and the bundle just falls back to the system
// roots plus configured caCerts.
func systemKeychainCerts() []byte {
	out, err := exec.Command("security", "find-certificate", "-a", "-p",
		"/Library/Keychains/System.keychain").Output()
	if err != nil {
		logger.Warn("could not read macOS System keychain CAs (continuing without them): " + err.Error())
		return nil
	}
	return out
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
	// runtime-managed balloon: the footprint follows the workload
	args = append(args, memoryPolicyCreateArgs(cfg)...)
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

// kubeconfigPath returns the kubeconfig file the merged cluster context is
// written into. It honours the KUBECONFIG environment variable (the first
// non-empty entry of the path list, matching kubectl's write semantics) and
// falls back to ~/.kube/config when KUBECONFIG is unset. Resolving the same
// path for both the merge and the subsequent use-context keeps them in sync:
// hardcoding ~/.kube/config while use-context read the ambient KUBECONFIG was
// what made the merge fail when KUBECONFIG pointed elsewhere.
func kubeconfigPath() (string, error) {
	for _, p := range filepath.SplitList(os.Getenv("KUBECONFIG")) {
		if p != "" {
			return p, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kube", "config"), nil
}

// KubeconfigMerge merges the cluster's kubeconfig into the active kubeconfig
// (KUBECONFIG, or ~/.kube/config) and switches the current context.
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
	kubeConfig, err := kubeconfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(kubeConfig), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(kubeConfig); err == nil {
		if data, err := os.ReadFile(kubeConfig); err == nil {
			_ = os.MkdirAll(cfg.RunDir(), 0o755)
			_ = os.WriteFile(filepath.Join(cfg.RunDir(), "kubeconfig.backup"), data, 0o600)
		}
		merge := exec.Command("kubectl", "config", "view", "--flatten")
		merge.Env = append(os.Environ(), "KUBECONFIG="+tmp.Name()+string(os.PathListSeparator)+kubeConfig)
		merged, err := merge.Output()
		if err != nil {
			detail := ""
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				detail = ": " + strings.TrimSpace(string(exitErr.Stderr))
			}
			return fmt.Errorf("kubeconfig merge failed: %w%s", err, detail)
		}
		if err := writeFileAtomic(kubeConfig, merged, 0o600); err != nil {
			return err
		}
	} else {
		if err := writeFileAtomic(kubeConfig, []byte(kc), 0o600); err != nil {
			return err
		}
	}
	// Pin use-context to the file we just wrote. If we relied on the ambient
	// KUBECONFIG instead, a KUBECONFIG that lists other files first would not
	// see the freshly merged context and this would fail with exit status 1.
	use := exec.Command("kubectl", "config", "use-context", cfg.KubeContext)
	use.Env = append(os.Environ(), "KUBECONFIG="+kubeConfig)
	if out, err := use.CombinedOutput(); err != nil {
		return fmt.Errorf("kubectl config use-context failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by an atomic rename, so a crash or disk-full mid-write can never
// truncate an existing file. This matters for ~/.kube/config, which k3c does
// not own — it holds all the user's other clusters' contexts.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
	// manage the kernel before the node VM is created: the bundled 16K
	// kernel for host memory return (default), or the recommended 4K kata
	// kernel for amd64/Rosetta workloads (cluster.kernel: recommended)
	EnsureClusterKernel(cfg)
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
	// install the containerd discard_unpacked_layers template before the node
	// is fully up so it is in place when k3s regenerates containerd's config
	installContainerdConfig(cfg)
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
	if err := convertClusterMemory(cfg); err != nil {
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

// Repair rebuilds the host->guest gateway forwarding plane without recreating
// the cluster (all state is preserved). Use it when host listeners report "up"
// but the gateway path (e.g. registry :5001 or the admission webhook :9443 on
// the vmnet gateway) returns EOF — typically after long uptime or host
// sleep/resume. A `daemons restart` only re-binds the host listeners; the dead
// path is the VM's network attachment (the vmnet bridge and, with transparent
// egress, the per-VM gvnet netstack), which only a VM restart rebuilds.
func Repair(cfg *config.Config) error {
	if !containerExists(cfg.ServerName, false) {
		return fmt.Errorf("cluster '%s' does not exist; create it first", cfg.Cluster)
	}
	resumeIfPaused(cfg)
	logger.Info("repairing gateway forwarding for cluster '" + cfg.Cluster + "'")

	// Force a fresh host-daemon spawn (re-binds listeners, reaps orphans). Start
	// (below) re-spawns them since their pidfiles are now gone.
	StopDaemons(cfg)

	// Restart the server VM so its network attachment is rebuilt. Stopping it
	// disconnects the VM; then tear down the transparent-egress netstack so the
	// next start spawns a FRESH one — ensureGvnet keeps a netstack whose pid is
	// alive and socket present, which is exactly the stale-but-running state we
	// are repairing, so it must be removed explicitly.
	if containerExists(cfg.ServerName, true) {
		logger.Info("restarting the server VM to rebuild its network attachment")
		_, _ = runContainer("stop", cfg.ServerName)
	}
	if cfg.TransparentEgress {
		stopGvnet(cfg, cfg.ServerName)
	}

	// Start re-spawns the daemons, respawns the netstack and re-attaches the VM
	// (startServerVM -> ensureGvnet), re-merges the kubeconfig, and via postStart
	// repairs virtiofs and re-registers the admission webhook, then waits Ready.
	return Start(cfg)
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
	// Re-merge ~/.kube/config before waiting on the API. A restart can change
	// the published API port, and a same-named context (e.g. a nested k3d
	// cluster reusing this name) can have clobbered the entry — either leaves a
	// stale endpoint that makes waitReady probe a dead port. KubeconfigMerge
	// rewrites the entry with the current port and switches context.
	if err := KubeconfigMerge(cfg); err != nil {
		return err
	}
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
	// VMs created before runtime memory-policy support carry no policy in
	// their configuration; arm it for this run
	applyMemoryPolicy(cfg, cfg.ServerName)
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
	// keep the containerd layer-discard template present across restarts and
	// snapshot restores; it takes effect on the next config regeneration
	installContainerdConfig(cfg)
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
	// Without this, a stopped container system (e.g. right after a host
	// restart) makes `container ls` fail and we'd print an empty table —
	// silently hiding existing clusters. Start the system first so the
	// listing reflects real state, like every other command does.
	if err := runtime.EnsureSystem(); err != nil {
		return err
	}
	clusters := Clusters(cfg)
	if ui.JSON() {
		return ui.EmitJSON(clusters)
	}
	rows := make([][]string, 0, len(clusters))
	for _, c := range clusters {
		current := ""
		if c.Active {
			current = "*"
		}
		rows = append(rows, []string{current, c.Name, c.Server, c.Registry, c.RAM, c.Context})
	}
	ui.Table(
		[]string{"", "NAME", "SERVER", "REGISTRY", "RAM", "CONTEXT"},
		rows,
		func(col int, val string) string {
			switch col {
			case 0:
				return ui.OK(val) // the * marker for the active cluster
			case 2, 3: // SERVER, REGISTRY states
				return ui.State(val)
			default:
				return val
			}
		},
	)
	return nil
}

// clusterRAM returns the OS-level physical memory footprint of the
// cluster's VM process (the Virtualization.framework process owns the
// guest memory; the supervisor's RSS is meaningless).
func clusterRAM(cluster string) string {
	return vmRAM(cluster + "-server")
}

// vmRAM returns the OS-level physical memory footprint of a VM's
// Virtualization.framework process (the guest memory owner), keyed by the
// VM's container name, or "-". Used for cluster servers and the docker
// sidecar alike.
func vmRAM(vmName string) string {
	pid := vzProcessPID(vmName)
	if pid == 0 {
		return "-"
	}
	out, err := runOut("footprint", strconv.Itoa(pid))
	if err != nil {
		return "-"
	}
	// Prefer the report header's "Footprint:" (dirty pages only). The
	// phys_footprint task counter also charges pages the balloon has already
	// returned to macOS as clean/reclaimable — after a warm snapshot's
	// suspend/resume that overstates a ~16G cluster as ~26G even though the
	// OS can take those pages back at any moment.
	if b, ok := scanFootprint(out, "Footprint:"); ok {
		return humanBytes(b)
	}
	if b, ok := scanFootprint(out, "phys_footprint:"); ok {
		return humanBytes(b)
	}
	return "-"
}

// scanFootprint scans footprint(1) output for "<marker> <value> <unit>" and
// converts the size to bytes. Rendered with humanBytes, the same formatter
// snapshot sizes use, so every size in the TUI/CLI reads identically.
func scanFootprint(out, marker string) (int64, bool) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == marker && i+2 < len(fields) {
				return parseFootprintBytes(fields[i+1], fields[i+2])
			}
		}
	}
	return 0, false
}

// parseFootprintBytes converts a footprint(1) "<value> <unit>" pair to a byte
// count. footprint reports binary units (1024-based) labelled KB/MB/GB — a VM's
// phys_footprint lands on an exact page-size multiple under 1024, not 1000.
func parseFootprintBytes(value, unit string) (int64, bool) {
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	mult := map[string]float64{
		"B":  1,
		"KB": 1 << 10,
		"MB": 1 << 20,
		"GB": 1 << 30,
		"TB": 1 << 40,
	}
	m, ok := mult[strings.ToUpper(unit)]
	if !ok {
		return 0, false
	}
	return int64(v * m), true
}

// Status prints daemon, container, and node state for a cluster.
// StatusInfo is the structured form behind `k3c status`.
type StatusInfo struct {
	Daemons    []NameState         `json:"daemons"`
	Paused     bool                `json:"paused"`
	Containers []map[string]string `json:"containers"`
	Nodes      []map[string]string `json:"nodes"`
}

// NameState pairs a daemon name with its running/stopped state.
type NameState struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func Status(cfg *config.Config) error {
	daemons := []NameState{
		{"proxy", pidState(cfg.ProxyPidFile())},
		{"sni-gateway", pidState(cfg.SNIPidFile())},
	}

	cOut, _ := runContainer("ls", "-a")
	cHead, cRows := parseColumns(cOut, func(fields []string) bool {
		return len(fields) > 0 && (fields[0] == cfg.ServerName || fields[0] == cfg.RegistryName)
	})
	nOut, _ := kubectl(cfg, "get", "nodes")
	nHead, nRows := parseColumns(nOut, nil)

	if ui.JSON() {
		return ui.EmitJSON(StatusInfo{
			Daemons:    daemons,
			Paused:     isPaused(cfg),
			Containers: rowMaps(cHead, cRows),
			Nodes:      rowMaps(nHead, nRows),
		})
	}

	ui.Section("host daemons")
	for _, d := range daemons {
		ui.KV(d.Name, ui.State(d.State), 12)
	}
	if isPaused(cfg) {
		fmt.Println("  " + ui.Warn("cluster is PAUSED (in memory; k3c cluster resume)"))
	}
	// STATE is column index 4 in `container ls` output.
	ui.Section("containers")
	renderColumns(cHead, cRows, map[int]bool{4: true})
	ui.Section("nodes")
	// STATUS is column index 1 in `kubectl get nodes` output.
	renderColumns(nHead, nRows, map[int]bool{1: true})
	return nil
}

// pidState maps a pidfile to "running"/"stopped".
func pidState(pidFile string) string {
	if pidAlive(pidFile) {
		return "running"
	}
	return "stopped"
}

// parseColumns splits whitespace-delimited command table output into its
// header fields and data rows. When keep is non-nil, only rows whose fields
// satisfy it are returned. Empty input yields nil, nil.
func parseColumns(out string, keep func(fields []string) bool) ([]string, [][]string) {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, nil
	}
	header := strings.Fields(lines[0])
	var rows [][]string
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if keep == nil || keep(fields) {
			rows = append(rows, fields)
		}
	}
	return header, rows
}

// renderColumns prints a parsed table, colorizing the state columns flagged in
// stateCols. An empty body prints a muted placeholder.
func renderColumns(header []string, rows [][]string, stateCols map[int]bool) {
	if len(header) == 0 {
		fmt.Println("  " + ui.Muted("(unavailable)"))
		return
	}
	if len(rows) == 0 {
		fmt.Println("  " + ui.Muted("(none)"))
		return
	}
	ui.Table(header, rows, func(col int, val string) string {
		if stateCols[col] {
			return ui.State(val)
		}
		return val
	})
}

// rowMaps zips each row against the header into a name->value map for JSON.
func rowMaps(header []string, rows [][]string) []map[string]string {
	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		m := map[string]string{}
		for i, h := range header {
			if i < len(r) {
				m[h] = r[i]
			}
		}
		out = append(out, m)
	}
	return out
}
