// Package config resolves the layered k3c configuration:
//
//	built-in defaults
//	~/.config/k3c/config.yaml   user defaults (e.g. corporate CA, mirrors)
//	./k3c.yaml                  project config (or --config / K3C_CONFIG)
//
// Set fields replace the layer below entirely (lists are not merged).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"k3c/ui"

	"gopkg.in/yaml.v3"
)

// FileConfig is the schema of a k3c config file. Every field is optional;
// unset fields fall through to the previous layer.
type FileConfig struct {
	Cluster struct {
		Name          string `yaml:"name"`          // default cluster name
		Image         string `yaml:"image"`         // k3s image
		ContextPrefix string `yaml:"contextPrefix"` // kube context = <prefix><name>
		APIHost       string `yaml:"apiHost"`       // TLS SAN + kubeconfig server host
		ClusterCIDR   string `yaml:"clusterCidr"`
		ServiceCIDR   string `yaml:"serviceCidr"`
		CPUs          int    `yaml:"cpus"`   // default: all host cores
		Memory        string `yaml:"memory"` // e.g. 48G
		// strip CPU/memory requests from pods via admission webhook, so
		// charts with production sizing fit on a laptop
		IgnoreCPURequests    *bool    `yaml:"ignoreCpuRequests"`
		IgnoreMemoryRequests *bool    `yaml:"ignoreMemoryRequests"`
		ExtraK3sArgs         []string `yaml:"extraK3sArgs"`
		// node kernel parameters, merged over the built-in defaults
		// (raised inotify limits)
		Sysctls map[string]string `yaml:"sysctls"`
		// periodically return memory the cluster no longer uses to the
		// host (balloon-capable container builds): a duration like "10m",
		// or "off" (default: 10m). Superseded by memoryPolicy on container
		// builds with runtime memory-policy support.
		AutoReclaim string `yaml:"autoReclaim"`
		// continuous memory management of the VMs (memory-policy-capable
		// container builds): "auto" sizes the memory balloon to the
		// workload, returning unused memory to the host; "off" disables
		// (default: auto)
		MemoryPolicy string `yaml:"memoryPolicy"`
		// memory kept available for a VM above its workload with
		// memoryPolicy auto, e.g. "1500M" (default: the runtime's 1G)
		MemoryHeadroom string `yaml:"memoryHeadroom"`
		// convert freshly created VMs with one suspend/restore cycle so
		// boot-time memory returns to the host immediately: "on-create",
		// or "off" (default: off — the first suspend/snapshot converts)
		MemoryConvert string `yaml:"memoryConvert"`
		// guest kernel management: "bundled" (default; install the 16K-page
		// kernel shipped with k3c — best memory return, but Rosetta/amd64
		// images cannot run), "recommended" (the runtime's 4K kata kernel;
		// needed for amd64 images), or "keep" (never touch the kernel)
		Kernel string `yaml:"kernel"`
		// scheduling priority of the cluster VMs relative to interactive
		// apps: "low" (clamped below GUI apps, default) or "normal"
		CPUPriority string `yaml:"cpuPriority"`
	} `yaml:"cluster"`
	Ports struct {
		Ingress int `yaml:"ingress"` // host port the cluster's :443 is published on
		Proxy   int `yaml:"proxy"`   // host CONNECT proxy port
	} `yaml:"ports"`
	LocalRegistry struct {
		Enabled  *bool `yaml:"enabled"`
		HostPort int   `yaml:"hostPort"`
	} `yaml:"localRegistry"`
	// CA certificates (globs, relative to the config file) appended to the
	// node's registry CA bundle (/etc/rancher/k3s/ca-bundle.pem).
	CACerts []string `yaml:"caCerts"`
	Egress  struct {
		// Domains resolved to the host gateway inside the cluster; pod HTTPS
		// traffic for them goes through the SNI gateway to the real host.
		Domains []string `yaml:"domains"`
		// Domains routed to the local cluster ingress instead.
		IngressDomains []string `yaml:"ingressDomains"`
		// Additional TCP ports served by the SNI egress gateway (443 is
		// always served). TLS connections to egress domains on these
		// ports are routed by their SNI to the real host (e.g. almplus
		// on 13001).
		Ports []int `yaml:"ports"`
		// Static TCP forwards ("PORT:HOST:PORT"): connections to the
		// gateway port are spliced to the target through the host network,
		// without TLS/SNI parsing. For non-TLS protocols like HTTP CONNECT
		// proxies (e.g. "9480:proxy.example.com:9480" lets pods use the
		// corporate proxy of a CI config; the host's proxy client carries
		// it). The target host also needs an entry in domains so pods
		// resolve it to the gateway.
		Forwards []string `yaml:"forwards"`
		// Transparent enables the gvisor-tap-vsock userspace netstack for
		// transparent egress: each VM's NIC is backed by a host-side netstack
		// that re-originates every connection from host sockets, so corporate
		// egress works with no SNI gateway, no CoreDNS override and no
		// per-domain config. Opt-in; env K3C_TRANSPARENT_EGRESS also enables it.
		Transparent *bool `yaml:"transparent"`
	} `yaml:"egress"`
	// Pull-through cache: a host-side registry cache serving as first
	// mirror endpoint for every configured registry. Transparent for the
	// cluster, shared across all clusters, falls back to the real
	// upstreams when down. Takes effect on cluster create.
	PullCache struct {
		Enabled *bool `yaml:"enabled"`
		Port    int   `yaml:"port"` // default 5011
		// images not pulled within this many days are pruned daily by the
		// host daemons (default 14, -1 disables the automatic prune)
		RetentionDays int `yaml:"retentionDays"`
	} `yaml:"pullCache"`
	// Docker sidecar: a docker:dind VM managed by k3c, providing a real
	// Docker Engine API (DOCKER_HOST) for Testcontainers, docker CLI, and
	// friends. Pulls go through the k3c proxy and pull cache.
	Docker struct {
		Enabled *bool  `yaml:"enabled"`
		CPUs    int    `yaml:"cpus"`   // default 4
		Memory  string `yaml:"memory"` // default 8G
		Port    int    `yaml:"port"`   // engine API port, default 2375
		// docker CLI context created and activated on `docker up` (and
		// restored to default on `docker down`); default "k3c", "off"
		// disables context management
		Context string `yaml:"context"`
		// k3s/k3d node images to prepare for nested k3d clusters. On
		// `docker up` k3c bakes the corporate CA (caCerts) into each, at the
		// sidecar's native architecture, so `k3d cluster create` works with an
		// unmodified k3d config: a `--volume cert:/etc/ssl/certs/...` mount
		// cannot inject the CA through the sidecar (the host path is absent in
		// the VM, so docker mounts an empty dir), and an emulated-amd64 node
		// breaks containerd's seccomp detection. See cluster/nodeprep.go.
		K3sNodeImages []string `yaml:"k3sNodeImages"`
	} `yaml:"docker"`
	// Verbatim k3s registries.yaml content (mirrors, auth, TLS).
	Registries string `yaml:"registries"`
	// Path to the Apple `container` CLI (default: container from PATH).
	// Point this at a fork to use features like pause/resume/suspend.
	ContainerBinary string `yaml:"containerBinary"`
	// Terminal UI appearance. Every color is optional; an unset color falls
	// back to the built-in default palette (see tui.newTheme).
	UI struct {
		Theme struct {
			Accent string `yaml:"accent"` // main/accent: title, selection, borders
			Dim    string `yaml:"dim"`    // muted text and separators
			Good   string `yaml:"good"`   // ok / running
			Warn   string `yaml:"warn"`   // warning / paused
			Cool   string `yaml:"cool"`   // secondary accent: keys, suspended
			Bad    string `yaml:"bad"`    // error / stopped
		} `yaml:"theme"`
	} `yaml:"ui"`
}

// Config is the effective, resolved configuration.
type Config struct {
	Cluster      string
	ServerName   string
	RegistryName string
	KubeContext  string

	Image       string
	APIHost     string
	ClusterCIDR string
	ServiceCIDR string
	CPUs        string
	Memory      string

	IgnoreCPURequests    bool
	IgnoreMemoryRequests bool

	Sysctls map[string]string

	ExtraK3sArgs []string

	VmnetGateway string
	ProxyPort    string
	IngressPort  string

	// per-cluster private host ports (set from the cluster state by the
	// cluster package; default to the legacy shared ports)
	APIPortInternal      string
	IngressPortInternal  string
	RegistryPortInternal string

	RegistryEnabled bool
	RegistryPort    string

	CACertGlobs    []string
	EgressDomains  []string
	EgressPorts    []int     // extra SNI gateway ports (443 always served)
	EgressForwards []Forward // static TCP forwards (no SNI parsing)
	IngressDomains []string
	Registries     string

	// TransparentEgress drives VMs through a per-VM gvisor-tap-vsock netstack
	// instead of the SNI gateway / CONNECT proxy (see Egress.Transparent).
	TransparentEgress bool

	ContainerBinary string // the Apple container CLI to use
	AutoReclaim     string // auto-reclaim interval ("off" disables)
	MemoryPolicy    string // "auto" (default) or "off"
	MemoryHeadroom  string // guest memory kept available above the workload ("" = runtime default)
	MemoryConvert   string // "on-create" or "off" (default)
	Kernel          string // "bundled" (default), "recommended", or "keep"
	CPUPriority     string // "low" (default) or "normal"

	PullCacheEnabled   bool
	PullCachePort      string
	PullCacheRetention int // days; 0 disables the automatic prune

	DockerEnabled       bool
	DockerCPUs          string
	DockerMemory        string
	DockerPort          string
	DockerContext       string   // docker CLI context name ("off" disables)
	DockerK3sNodeImages []string // k3s node images to bake the CA into for nested k3d

	BaseDir    string // state directory (~/.config/k3c)
	ConfigFile string // project config in effect, for daemon respawn

	// Theme overrides for the terminal UI. Empty fields mean "use the
	// built-in default" and are resolved by the TUI (tui.newTheme).
	Theme UITheme
}

// UITheme holds the optional terminal-UI color overrides. Each value is a
// lipgloss color string (hex "#RRGGBB" or an ANSI index); empty means the
// built-in default is used.
type UITheme struct {
	Accent string `json:"accent"`
	Dim    string `json:"dim"`
	Good   string `json:"good"`
	Warn   string `json:"warn"`
	Cool   string `json:"cool"`
	Bad    string `json:"bad"`
}

// truthyEnv reports whether an environment variable is set to a truthy value.
func truthyEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}

// pullCacheRetention resolves the configured retention days: default 14,
// negative values disable the automatic prune (0 in the resolved config).
func pullCacheRetention(days int) int {
	switch {
	case days < 0:
		return 0
	case days == 0:
		return 14
	default:
		return days
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadFile(path string) (FileConfig, error) {
	var fc FileConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return fc, err
	}
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return fc, fmt.Errorf("%s: %w", path, err)
	}
	// CA globs are relative to the file that declares them
	dir := filepath.Dir(path)
	for i, g := range fc.CACerts {
		if !filepath.IsAbs(g) {
			fc.CACerts[i] = filepath.Join(dir, g)
		}
	}
	return fc, nil
}

// merge overlays src onto dst: set fields in src win.
func merge(dst *FileConfig, src FileConfig) {
	s := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	i := func(dst *int, v int) {
		if v != 0 {
			*dst = v
		}
	}
	l := func(dst *[]string, v []string) {
		if len(v) > 0 {
			*dst = v
		}
	}
	s(&dst.Cluster.Name, src.Cluster.Name)
	s(&dst.Cluster.Image, src.Cluster.Image)
	s(&dst.Cluster.ContextPrefix, src.Cluster.ContextPrefix)
	s(&dst.Cluster.APIHost, src.Cluster.APIHost)
	s(&dst.Cluster.ClusterCIDR, src.Cluster.ClusterCIDR)
	s(&dst.Cluster.ServiceCIDR, src.Cluster.ServiceCIDR)
	i(&dst.Cluster.CPUs, src.Cluster.CPUs)
	s(&dst.Cluster.Memory, src.Cluster.Memory)
	if src.Cluster.IgnoreCPURequests != nil {
		dst.Cluster.IgnoreCPURequests = src.Cluster.IgnoreCPURequests
	}
	if src.Cluster.IgnoreMemoryRequests != nil {
		dst.Cluster.IgnoreMemoryRequests = src.Cluster.IgnoreMemoryRequests
	}
	l(&dst.Cluster.ExtraK3sArgs, src.Cluster.ExtraK3sArgs)
	for k, v := range src.Cluster.Sysctls {
		if dst.Cluster.Sysctls == nil {
			dst.Cluster.Sysctls = map[string]string{}
		}
		dst.Cluster.Sysctls[k] = v
	}
	i(&dst.Ports.Ingress, src.Ports.Ingress)
	i(&dst.Ports.Proxy, src.Ports.Proxy)
	if src.LocalRegistry.Enabled != nil {
		dst.LocalRegistry.Enabled = src.LocalRegistry.Enabled
	}
	i(&dst.LocalRegistry.HostPort, src.LocalRegistry.HostPort)
	s(&dst.ContainerBinary, src.ContainerBinary)
	s(&dst.Cluster.AutoReclaim, src.Cluster.AutoReclaim)
	s(&dst.Cluster.MemoryPolicy, src.Cluster.MemoryPolicy)
	s(&dst.Cluster.MemoryHeadroom, src.Cluster.MemoryHeadroom)
	s(&dst.Cluster.MemoryConvert, src.Cluster.MemoryConvert)
	s(&dst.Cluster.Kernel, src.Cluster.Kernel)
	s(&dst.Cluster.CPUPriority, src.Cluster.CPUPriority)
	l(&dst.CACerts, src.CACerts)
	l(&dst.Egress.Domains, src.Egress.Domains)
	if len(src.Egress.Ports) > 0 {
		dst.Egress.Ports = src.Egress.Ports
	}
	l(&dst.Egress.Forwards, src.Egress.Forwards)
	if src.PullCache.Enabled != nil {
		dst.PullCache.Enabled = src.PullCache.Enabled
	}
	i(&dst.PullCache.Port, src.PullCache.Port)
	i(&dst.PullCache.RetentionDays, src.PullCache.RetentionDays)
	if src.Docker.Enabled != nil {
		dst.Docker.Enabled = src.Docker.Enabled
	}
	i(&dst.Docker.CPUs, src.Docker.CPUs)
	s(&dst.Docker.Memory, src.Docker.Memory)
	i(&dst.Docker.Port, src.Docker.Port)
	s(&dst.Docker.Context, src.Docker.Context)
	l(&dst.Docker.K3sNodeImages, src.Docker.K3sNodeImages)
	l(&dst.Egress.IngressDomains, src.Egress.IngressDomains)
	if src.Egress.Transparent != nil {
		dst.Egress.Transparent = src.Egress.Transparent
	}
	s(&dst.Registries, src.Registries)
	s(&dst.UI.Theme.Accent, src.UI.Theme.Accent)
	s(&dst.UI.Theme.Dim, src.UI.Theme.Dim)
	s(&dst.UI.Theme.Good, src.UI.Theme.Good)
	s(&dst.UI.Theme.Warn, src.UI.Theme.Warn)
	s(&dst.UI.Theme.Cool, src.UI.Theme.Cool)
	s(&dst.UI.Theme.Bad, src.UI.Theme.Bad)
}

// UserConfigDir returns ~/.config/k3c (honoring XDG_CONFIG_HOME). It holds
// the user config file and all k3c state.
// StateDir is the k3c state directory (K3C_BASE_DIR or the user config
// directory).
func StateDir() string {
	if dir := os.Getenv("K3C_BASE_DIR"); dir != "" {
		return dir
	}
	return UserConfigDir()
}

func UserConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "k3c")
}

// Resolve layers the config files and applies built-in defaults.
// projectPath comes from --config / K3C_CONFIG, defaulting to ./k3c.yaml.
// Forward is a static TCP forward served by the egress gateway: traffic to
// the gateway port is spliced to the target through the host network.
type Forward struct {
	Port   string // gateway listen port
	Target string // host:port dialed on the host side
}

// parseForwards parses "PORT:HOST:PORT" forward declarations.
func parseForwards(specs []string) ([]Forward, error) {
	var forwards []Forward
	for _, spec := range specs {
		port, target, ok := strings.Cut(spec, ":")
		if _, err := strconv.Atoi(port); !ok || err != nil || !strings.Contains(target, ":") {
			return nil, fmt.Errorf("invalid egress forward %q (want PORT:HOST:PORT)", spec)
		}
		forwards = append(forwards, Forward{Port: port, Target: target})
	}
	return forwards, nil
}

func Resolve(cluster, projectPath string) (*Config, error) {
	var fc FileConfig

	if dir := UserConfigDir(); dir != "" {
		if user, err := loadFile(filepath.Join(dir, "config.yaml")); err == nil {
			merge(&fc, user)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	baseDir := StateDir()
	if baseDir == "" {
		return nil, fmt.Errorf("cannot determine state directory; set K3C_BASE_DIR")
	}

	explicit := projectPath != ""
	if projectPath == "" {
		projectPath = envOr("K3C_CONFIG", "k3c.yaml")
		explicit = os.Getenv("K3C_CONFIG") != ""
	}
	configFile := ""
	if project, err := loadFile(projectPath); err == nil &&
		(explicit || cluster == "" || project.Cluster.Name == "" || project.Cluster.Name == cluster) {
		// an implicitly found ./k3c.yaml only applies to its own cluster:
		// starting a DIFFERENT named cluster from this directory must not
		// inherit (or overwrite) this project's settings
		merge(&fc, project)
		if abs, err := filepath.Abs(projectPath); err == nil {
			configFile = abs
		}
	} else if err != nil && (explicit || !os.IsNotExist(err)) {
		return nil, err
	} else if cluster != "" {
		// no project config applies here, but the cluster may have one
		// persisted from create — lets start/stop/kubeconfig work from any
		// directory
		persisted := filepath.Join(baseDir, "clusters", cluster, "k3c.yaml")
		if project, err := loadFile(persisted); err == nil {
			merge(&fc, project)
			configFile = persisted
		}
	}

	if cluster == "" {
		cluster = fc.Cluster.Name
	}
	if cluster == "" {
		cluster = "k3c"
	}

	def := func(v, fallback string) string {
		if v == "" {
			return fallback
		}
		return v
	}
	cpus := fc.Cluster.CPUs
	if cpus == 0 {
		cpus = runtime.NumCPU()
	}
	// the docker sidecar defaults to all host cores too (not a small fixed
	// count): nested k3d schedules the same workloads as the native cluster,
	// whose pod CPU requests are not stripped by k3c's admission webhook, so
	// the node needs the host's full allocatable CPU (matching other macOS
	// docker runtimes that expose all cores).
	dockerCPUs := fc.Docker.CPUs
	if dockerCPUs == 0 {
		dockerCPUs = runtime.NumCPU()
	}
	port := func(v, fallback int) string {
		if v == 0 {
			v = fallback
		}
		return strconv.Itoa(v)
	}

	// the kernel defaults (128/8192) are far too low for a node full of
	// file-watching workloads; kind/k3d raise them the same way
	sysctls := map[string]string{
		"fs.inotify.max_user_instances": "1024",
		"fs.inotify.max_user_watches":   "1048576",
	}
	for k, v := range fc.Cluster.Sysctls {
		sysctls[k] = v
	}

	contextPrefix := def(fc.Cluster.ContextPrefix, "k3c-")
	forwards, err := parseForwards(fc.Egress.Forwards)
	if err != nil {
		return nil, err
	}
	return &Config{
		Cluster:              cluster,
		ServerName:           cluster + "-server",
		RegistryName:         cluster + "-registry",
		KubeContext:          contextPrefix + cluster,
		Image:                def(fc.Cluster.Image, "docker.io/rancher/k3s:v1.33.9-k3s1"),
		APIHost:              def(fc.Cluster.APIHost, "127.0.0.1"),
		ClusterCIDR:          def(fc.Cluster.ClusterCIDR, "10.42.0.0/16"),
		ServiceCIDR:          def(fc.Cluster.ServiceCIDR, "10.43.0.0/16"),
		CPUs:                 strconv.Itoa(cpus),
		Memory:               def(fc.Cluster.Memory, "8G"),
		ExtraK3sArgs:         fc.Cluster.ExtraK3sArgs,
		Sysctls:              sysctls,
		IgnoreCPURequests:    fc.Cluster.IgnoreCPURequests != nil && *fc.Cluster.IgnoreCPURequests,
		IgnoreMemoryRequests: fc.Cluster.IgnoreMemoryRequests != nil && *fc.Cluster.IgnoreMemoryRequests,
		VmnetGateway:         "192.168.64.1",
		ProxyPort:            port(fc.Ports.Proxy, 3128),
		IngressPort:          port(fc.Ports.Ingress, 8444),
		RegistryEnabled:      fc.LocalRegistry.Enabled != nil && *fc.LocalRegistry.Enabled,
		RegistryPort:         port(fc.LocalRegistry.HostPort, 5001),
		CACertGlobs:          fc.CACerts,
		EgressDomains:        fc.Egress.Domains,
		EgressPorts:          fc.Egress.Ports,
		EgressForwards:       forwards,
		IngressDomains:       fc.Egress.IngressDomains,
		// Default ON: transparent egress gives VMs (and the docker sidecar's
		// build containers) real DNS + egress, so `RUN`/npm/`ADD http://…` work
		// like a normal Docker host. Set egress.transparent:false for the legacy
		// SNI-gateway / CONNECT-proxy mode.
		TransparentEgress:   fc.Egress.Transparent == nil || *fc.Egress.Transparent || truthyEnv("K3C_TRANSPARENT_EGRESS"),
		PullCacheEnabled:    fc.PullCache.Enabled != nil && *fc.PullCache.Enabled,
		PullCachePort:       port(fc.PullCache.Port, 5011),
		PullCacheRetention:  pullCacheRetention(fc.PullCache.RetentionDays),
		DockerEnabled:       fc.Docker.Enabled != nil && *fc.Docker.Enabled,
		DockerCPUs:          strconv.Itoa(dockerCPUs),
		DockerMemory:        def(fc.Docker.Memory, "8G"),
		DockerPort:          port(fc.Docker.Port, 2375),
		DockerContext:       def(fc.Docker.Context, "k3c"),
		DockerK3sNodeImages: fc.Docker.K3sNodeImages,
		Registries:          fc.Registries,
		ContainerBinary:     def(fc.ContainerBinary, "container"),
		AutoReclaim:         def(fc.Cluster.AutoReclaim, "10m"),
		MemoryPolicy:        def(fc.Cluster.MemoryPolicy, "auto"),
		MemoryHeadroom:      fc.Cluster.MemoryHeadroom,
		MemoryConvert:       def(fc.Cluster.MemoryConvert, "off"),
		Kernel:              def(fc.Cluster.Kernel, "bundled"),
		CPUPriority:         def(fc.Cluster.CPUPriority, "low"),
		BaseDir:             baseDir,
		ConfigFile:          configFile,
		Theme: UITheme{
			Accent: fc.UI.Theme.Accent,
			Dim:    fc.UI.Theme.Dim,
			Good:   fc.UI.Theme.Good,
			Warn:   fc.UI.Theme.Warn,
			Cool:   fc.UI.Theme.Cool,
			Bad:    fc.UI.Theme.Bad,
		},
	}, nil
}

func (c *Config) RunDir() string        { return filepath.Join(c.BaseDir, "clusters", c.Cluster) }
func (c *Config) K3sEtcDir() string     { return filepath.Join(c.RunDir(), "k3s-etc") }
func (c *Config) ImagesDir() string     { return filepath.Join(c.RunDir(), "images") }
func (c *Config) ProxyPidFile() string  { return filepath.Join(c.BaseDir, "proxy.pid") }
func (c *Config) SNIPidFile() string    { return filepath.Join(c.BaseDir, "sni-gateway.pid") }
func (c *Config) DaemonLogFile() string { return filepath.Join(c.BaseDir, "daemons.log") }

// ContextPrefix returns the configured kube context prefix.
func (c *Config) ContextPrefix() string {
	return strings.TrimSuffix(c.KubeContext, c.Cluster)
}

// K3sCommand builds the in-container startup script. modernKernel reports
// whether the node's kernel has br_netfilter and vxlan (the recommended kata
// 6.18+ kernel does; the older 6.12.28-153 does not) — see
// cluster.KernelHasModernNetfilter.
//
// k3s' bundled iptables wrapper (iptables-detect.sh) picks the nft backend on
// a kernel with no pre-existing rules, which killed kube-proxy on the old
// nftables-less kernel; forcing the legacy backend (as kindest/node does) is
// harmless on both and kept unconditionally.
//
// On the OLD kernel two more workarounds are needed: it lacks vxlan so flannel
// must use host-gw (fine: single node), and it lacks br_netfilter so same-node
// ClusterIP replies bypass iptables un-NAT on the flannel bridge and get
// dropped (breaking pod DNS) — masquerade-all forces service traffic through
// the node instead. The modern kernel needs neither: flannel uses its default
// (vxlan) and DNAT works natively.
// GvnetRouteSnippet is a shell snippet for a VM entrypoint (transparent egress)
// that repoints the guest at the gvnet NIC — the second, egress NIC — while the
// vmnet NIC stays primary for host<->VM (published ports, containerIP). It
// finds the sole 192.168.x interface that is not the vmnet subnet
// (192.168.64.x), uses that subnet's .1 as the gateway, makes it the default
// route, and points DNS at it: the gvnet netstack's resolver re-originates
// queries from the host (the vmnet gateway does not resolve external names).
// For k3s this becomes the CoreDNS upstream, so pods resolve + egress too.
const GvnetRouteSnippet = `GV=$(ip -4 -o addr show | awk '$4 !~ /^192[.]168[.]64[.]/ && $4 ~ /^192[.]168[.]/ {print $2" "$4; exit}'); if [ -n "$GV" ]; then GVGW=$(echo "${GV#* }" | awk -F'[./]' '{print $1"."$2"."$3".1"}'); ip route replace default via "$GVGW" dev "${GV%% *}"; echo "nameserver $GVGW" > /etc/resolv.conf; fi
`

// CATrustSnippet is a shell snippet for a VM entrypoint that installs the
// mounted CA bundle (/k3c-ca/ca-bundle.pem — host system roots plus any
// configured caCerts) into the guest's OS trust store before the daemon starts.
// SSL_CERT_FILE already points Go (dockerd) at that bundle, but nothing else in
// the VM — BuildKit, containerd, curl/apk in nested builds — reads it; those
// consult /etc/ssl/certs/ca-certificates.crt instead. Sharing the same bundle
// system-wide (via Alpine's update-ca-certificates) makes every component trust
// exactly what the host does, so e.g. a `docker push` to a corporate-CA
// registry verifies. It hard-codes no specific CA; it just re-shares the bundle.
// Idempotent (fixed filename, safe to re-run) and a no-op when the bundle or
// update-ca-certificates is absent.
const CATrustSnippet = `if [ -f /k3c-ca/ca-bundle.pem ]; then mkdir -p /usr/local/share/ca-certificates; cp /k3c-ca/ca-bundle.pem /usr/local/share/ca-certificates/k3c-ca-bundle.crt; command -v update-ca-certificates >/dev/null 2>&1 && update-ca-certificates >/dev/null 2>&1 || true; fi
`

func (c *Config) K3sCommand(modernKernel bool) string {
	args := []string{
		"--disable=traefik",
		"--cluster-cidr=" + c.ClusterCIDR,
		"--service-cidr=" + c.ServiceCIDR,
		"--tls-san=127.0.0.1",
	}
	if !modernKernel {
		args = append(args, "--flannel-backend=host-gw", "--kube-proxy-arg=masquerade-all=true")
	}
	if c.APIHost != "127.0.0.1" {
		args = append(args, "--tls-san="+c.APIHost)
	}
	var prefix string
	if c.TransparentEgress {
		// Dual-NIC: the gvnet NIC owns the default route (transparent egress),
		// so pin k3s to the vmnet NIC for the node IP and flannel — that IP
		// stays host-routable (published API port, kubelet) while egress goes
		// out gvnet. Resolve the vmnet NIC/IP at boot (the runtime assigns it).
		args = append(args, "--node-ip=$K3C_NODE_IP", "--flannel-iface=$K3C_VMNET_IF")
		prefix = `K3C_VMNET_IF=$(ip -4 -o addr show | awk '/192[.]168[.]64[.]/{print $2; exit}')
K3C_NODE_IP=$(ip -4 -o addr show | awk '/192[.]168[.]64[.]/{split($4,a,"/"); print a[1]; exit}')
` + GvnetRouteSnippet
	}
	// The whole app stack schedules at once on a single node, so the kubelet's
	// default image-pull rate limit (registryPullQPS=5, burst=10) rejects pulls
	// with "pull QPS exceeded" and churns through ImagePullBackOff until the
	// budget refills. Disable it (0 = unlimited); pulls stay serialized by
	// default, so this drops the self-inflicted backoff without flooding the
	// registry/mirror. Overridable via extraK3sArgs (later args win).
	//
	// The KubeletConfiguration fields registryPullQPS/registryBurst map to the
	// kubelet CLI flags --registry-qps/--registry-burst (there is no
	// --registry-pull-qps flag; passing it makes the kubelet exit on an unknown
	// flag and the node never goes Ready).
	args = append(args, "--kubelet-arg=registry-qps=0", "--kubelet-arg=registry-burst=0")
	args = append(args, c.ExtraK3sArgs...)
	// Seed mode: when the host drops a marker into the (bind-mounted)
	// /etc/rancher/k3s, boot the VM WITHOUT starting k3s and just idle. A
	// frozen restore uses this to inject the datastore/PVC/creds into the
	// rootfs while k3s is not holding them — exec into a running k3s hangs, and
	// k3s is PID 1 so it cannot be stopped from inside without killing the VM.
	return `if [ -f /etc/rancher/k3s/` + SeedModeMarker + ` ]; then echo "k3c: seed mode — k3s not started"; exec sleep infinity; fi
for b in iptables iptables-save iptables-restore ip6tables ip6tables-save ip6tables-restore; do
	ln -sf xtables-legacy-multi /bin/aux/$b
done
` + prefix + c.sysctlCommands() + `exec k3s server ` + strings.Join(args, " ") + "\n"
}

// SeedModeMarker is the filename (under the bind-mounted /etc/rancher/k3s) that
// tells the server entrypoint to idle instead of starting k3s. See K3sCommand.
const SeedModeMarker = ".seed-mode"

// sysctlCommands renders the node kernel parameter setup.
func (c *Config) sysctlCommands() string {
	keys := make([]string, 0, len(c.Sysctls))
	for k := range c.Sysctls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "sysctl -w %s=%s\n", k, c.Sysctls[k])
	}
	return b.String()
}

// CorednsCustom renders the CoreDNS override that resolves the egress
// domains to the host gateway (empty if no domains are configured).
//
// A "*" entry resolves EVERY external name to the gateway. The template
// plugin runs before the kubernetes and hosts plugins, so the catch-all
// would shadow in-cluster DNS; never-matching templates for the cluster
// zones and the node name fall those queries through to the next plugin.
func (c *Config) CorednsCustom() string {
	// Transparent egress resolves real DNS (via the per-VM netstack) and
	// connects directly, so the egress-domain override is neither needed nor
	// wanted — pods must not have external names rewritten to a gateway.
	if c.TransparentEgress {
		return ""
	}
	if len(c.EgressDomains) == 0 {
		return ""
	}
	if slices.Contains(c.EgressDomains, "*") {
		return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  egress.override: |
    template IN ANY cluster.local in-addr.arpa ip6.arpa %[2]s {
        match "^k3c[.]hole[.]$"
        fallthrough
    }
    template IN A . {
        answer "{{ .Name }} 60 IN A %[1]s"
    }
    template IN AAAA . {
        rcode NOERROR
    }
`, c.VmnetGateway, c.ServerName)
	}
	zones := strings.Join(c.EgressDomains, " ")
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  egress.override: |
    template IN A %[1]s {
        answer "{{ .Name }} 60 IN A %[2]s"
    }
    template IN AAAA %[1]s {
        rcode NOERROR
    }
`, zones, c.VmnetGateway)
}

// k3sRegistries is the subset of the k3s registries.yaml schema needed
// for the pull-through cache rewrite.
type k3sRegistries struct {
	Mirrors map[string]struct {
		Endpoint []string `yaml:"endpoint"`
	} `yaml:"mirrors"`
	Configs map[string]any `yaml:"configs"`
}

// RegistryUpstreams maps every configured registry host to its upstream
// endpoints (mirrors as configured; configs-only hosts to themselves).
func RegistryUpstreams(registries string) map[string][]string {
	var parsed k3sRegistries
	_ = yaml.Unmarshal([]byte(registries), &parsed)
	upstreams := map[string][]string{}
	for host, mirror := range parsed.Mirrors {
		if len(mirror.Endpoint) > 0 {
			upstreams[host] = mirror.Endpoint
		}
	}
	for host := range parsed.Configs {
		if _, ok := upstreams[host]; !ok {
			upstreams[host] = []string{"https://" + host}
		}
	}
	return upstreams
}

// EffectiveRegistries returns the registries.yaml content written to the
// node. With the pull cache enabled, every registry gets the cache as its
// first mirror endpoint and its real upstreams as fallback; local
// endpoints (the dev registry on the vmnet gateway) stay untouched.
func (c *Config) EffectiveRegistries() string {
	if !c.PullCacheEnabled || c.Registries == "" {
		return c.Registries
	}
	var parsed k3sRegistries
	if err := yaml.Unmarshal([]byte(c.Registries), &parsed); err != nil {
		return c.Registries
	}
	cacheEndpoint := "http://" + c.VmnetGateway + ":" + c.PullCachePort
	mirrors := map[string]map[string][]string{}
	addMirror := func(host string, upstream []string) {
		local := false
		for _, e := range upstream {
			if strings.Contains(e, c.VmnetGateway) || strings.Contains(e, "127.0.0.1") {
				local = true
			}
		}
		if local {
			mirrors[host] = map[string][]string{"endpoint": upstream}
			return
		}
		mirrors[host] = map[string][]string{"endpoint": append([]string{cacheEndpoint}, upstream...)}
	}
	for host, mirror := range parsed.Mirrors {
		addMirror(host, mirror.Endpoint)
	}
	for host := range parsed.Configs {
		if _, ok := mirrors[host]; !ok {
			addMirror(host, []string{"https://" + host})
		}
	}
	rewritten := map[string]any{"mirrors": mirrors}
	if len(parsed.Configs) > 0 {
		rewritten["configs"] = parsed.Configs
	}
	data, err := yaml.Marshal(rewritten)
	if err != nil {
		return c.Registries
	}
	return string(data)
}

// NoProxy lists destinations containerd must reach directly.
func (c *Config) NoProxy() string {
	return strings.Join([]string{
		c.ClusterCIDR, c.ServiceCIDR,
		".svc", ".cluster.local", "localhost", "127.0.0.1", "192.168.64.0/24",
	}, ",")
}

// Print shows the effective configuration (k3c config view).
// ConfigView is the curated, machine-readable form of the effective
// configuration behind `k3c config view --json`. It mirrors the fields Print
// shows rather than dumping the full internal Config.
type ConfigView struct {
	Cluster         string   `json:"cluster"`
	Context         string   `json:"context"`
	Image           string   `json:"image"`
	APIHost         string   `json:"apiHost"`
	ClusterCIDR     string   `json:"clusterCIDR"`
	ServiceCIDR     string   `json:"serviceCIDR"`
	CPUs            string   `json:"cpus"`
	Memory          string   `json:"memory"`
	IngressPort     string   `json:"ingressPort"`
	ProxyPort       string   `json:"proxyPort"`
	RegistryEnabled bool     `json:"registryEnabled"`
	RegistryPort    string   `json:"registryPort"`
	CACertGlobs     []string `json:"caCertGlobs"`
	EgressDomains   []string `json:"egressDomains"`
	IngressDomains  []string `json:"ingressDomains"`
	StateDir        string   `json:"stateDir"`
	ProjectConfig   string   `json:"projectConfig"`
	ContainerBinary string   `json:"containerBinary"`
	Theme           UITheme  `json:"theme"`
}

// View returns the curated configuration for JSON output.
func (c *Config) View() ConfigView {
	return ConfigView{
		Cluster:         c.Cluster,
		Context:         c.KubeContext,
		Image:           c.Image,
		APIHost:         c.APIHost,
		ClusterCIDR:     c.ClusterCIDR,
		ServiceCIDR:     c.ServiceCIDR,
		CPUs:            c.CPUs,
		Memory:          c.Memory,
		IngressPort:     c.IngressPort,
		ProxyPort:       c.ProxyPort,
		RegistryEnabled: c.RegistryEnabled,
		RegistryPort:    c.RegistryPort,
		CACertGlobs:     c.CACertGlobs,
		EgressDomains:   c.EgressDomains,
		IngressDomains:  c.IngressDomains,
		StateDir:        c.BaseDir,
		ProjectConfig:   c.ConfigFile,
		ContainerBinary: c.ContainerBinary,
		Theme:           c.Theme,
	}
}

func (c *Config) Print() {
	const w = 9
	orNone := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return ui.Muted("none")
		}
		return s
	}
	list := func(items []string) string {
		if len(items) == 0 {
			return ui.Muted("none")
		}
		return strings.Join(items, ", ")
	}

	ui.Section("cluster")
	ui.KV("name", c.Cluster, w)
	ui.KV("context", c.KubeContext, w)
	ui.KV("image", c.Image, w)
	ui.KV("api host", c.APIHost, w)
	ui.KV("resources", fmt.Sprintf("%s cpus, %s memory", c.CPUs, c.Memory), w)

	ui.Section("network")
	ui.KV("cidrs", fmt.Sprintf("cluster %s, service %s", c.ClusterCIDR, c.ServiceCIDR), w)
	ui.KV("ports", fmt.Sprintf("ingress %s, proxy %s", c.IngressPort, c.ProxyPort), w)
	ui.KV("egress", list(c.EgressDomains), w)
	ui.KV("ingress", list(c.IngressDomains), w)

	ui.Section("registry")
	ui.KV("local", ui.State(boolWord(c.RegistryEnabled))+" (port "+c.RegistryPort+")", w)
	ui.KV("mirrors", fmt.Sprintf("%d bytes configured", len(c.Registries)), w)

	ui.Section("tls / paths")
	ui.KV("ca certs", list(c.CACertGlobs), w)
	ui.KV("state", c.BaseDir, w)
	ui.KV("project", orNone(c.ConfigFile), w)

	orDefault := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return ui.Muted("default")
		}
		return s
	}
	ui.Section("ui theme")
	ui.KV("accent", orDefault(c.Theme.Accent), w)
	ui.KV("dim", orDefault(c.Theme.Dim), w)
	ui.KV("good", orDefault(c.Theme.Good), w)
	ui.KV("warn", orDefault(c.Theme.Warn), w)
	ui.KV("cool", orDefault(c.Theme.Cool), w)
	ui.KV("bad", orDefault(c.Theme.Bad), w)
}

// boolWord renders a bool as enabled/disabled for State colorization.
func boolWord(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}
