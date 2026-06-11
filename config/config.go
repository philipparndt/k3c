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
	"sort"
	"strconv"
	"strings"

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
	} `yaml:"egress"`
	// Verbatim k3s registries.yaml content (mirrors, auth, TLS).
	Registries string `yaml:"registries"`
	// Path to the Apple `container` CLI (default: container from PATH).
	// Point this at a fork to use features like pause/resume/suspend.
	ContainerBinary string `yaml:"containerBinary"`
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
	IngressDomains []string
	Registries     string

	ContainerBinary string // the Apple container CLI to use

	BaseDir    string // state directory (~/.config/k3c)
	ConfigFile string // project config in effect, for daemon respawn
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
	l(&dst.CACerts, src.CACerts)
	l(&dst.Egress.Domains, src.Egress.Domains)
	l(&dst.Egress.IngressDomains, src.Egress.IngressDomains)
	s(&dst.Registries, src.Registries)
}

// UserConfigDir returns ~/.config/k3c (honoring XDG_CONFIG_HOME). It holds
// the user config file and all k3c state.
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
func Resolve(cluster, projectPath string) (*Config, error) {
	var fc FileConfig

	if dir := UserConfigDir(); dir != "" {
		if user, err := loadFile(filepath.Join(dir, "config.yaml")); err == nil {
			merge(&fc, user)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	baseDir := os.Getenv("K3C_BASE_DIR")
	if baseDir == "" {
		baseDir = UserConfigDir()
	}
	if baseDir == "" {
		return nil, fmt.Errorf("cannot determine state directory; set K3C_BASE_DIR")
	}

	explicit := projectPath != ""
	if projectPath == "" {
		projectPath = envOr("K3C_CONFIG", "k3c.yaml")
		explicit = os.Getenv("K3C_CONFIG") != ""
	}
	configFile := ""
	if project, err := loadFile(projectPath); err == nil {
		merge(&fc, project)
		if abs, err := filepath.Abs(projectPath); err == nil {
			configFile = abs
		}
	} else if explicit || !os.IsNotExist(err) {
		return nil, err
	} else if cluster != "" {
		// no project config here, but the cluster may have one persisted
		// from create — lets start/stop/kubeconfig work from any directory
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
		IngressDomains:       fc.Egress.IngressDomains,
		Registries:           fc.Registries,
		ContainerBinary:      def(fc.ContainerBinary, "container"),
		BaseDir:              baseDir,
		ConfigFile:           configFile,
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

// K3sCommand builds the in-container startup script.
//
// The Apple container VM kernel has no nftables support, but k3s' bundled
// iptables wrapper (iptables-detect.sh) picks the nft backend on a kernel
// with no pre-existing rules, which kills kube-proxy. Force the legacy
// backend (same thing kindest/node does in its entrypoint), then start k3s.
// The kernel also lacks vxlan, so flannel uses host-gw (fine: single node).
// It lacks br_netfilter too, so same-node ClusterIP replies would bypass
// iptables un-NAT on the flannel bridge and get dropped (breaking e.g. all
// pod DNS); masquerade-all forces service traffic through the node instead.
func (c *Config) K3sCommand() string {
	args := []string{
		"--disable=traefik",
		"--cluster-cidr=" + c.ClusterCIDR,
		"--service-cidr=" + c.ServiceCIDR,
		"--tls-san=127.0.0.1",
		"--flannel-backend=host-gw",
		"--kube-proxy-arg=masquerade-all=true",
	}
	if c.APIHost != "127.0.0.1" {
		args = append(args, "--tls-san="+c.APIHost)
	}
	args = append(args, c.ExtraK3sArgs...)
	return `for b in iptables iptables-save iptables-restore ip6tables ip6tables-save ip6tables-restore; do
	ln -sf xtables-legacy-multi /bin/aux/$b
done
` + c.sysctlCommands() + `exec k3s server ` + strings.Join(args, " ") + "\n"
}

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
func (c *Config) CorednsCustom() string {
	if len(c.EgressDomains) == 0 {
		return ""
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

// NoProxy lists destinations containerd must reach directly.
func (c *Config) NoProxy() string {
	return strings.Join([]string{
		c.ClusterCIDR, c.ServiceCIDR,
		".svc", ".cluster.local", "localhost", "127.0.0.1", "192.168.64.0/24",
	}, ",")
}

// Print shows the effective configuration (k3c config view).
func (c *Config) Print() {
	fmt.Printf(`cluster:         %s (context: %s)
image:           %s
api host:        %s
cidrs:           cluster %s, service %s
resources:       %s cpus, %s memory
ports:           ingress %s, proxy %s
local registry:  enabled=%v port=%s
ca certs:        %s
egress domains:  %s
ingress domains: %s
registries:      %d bytes configured
state dir:       %s
project config:  %s
`, c.Cluster, c.KubeContext, c.Image, c.APIHost, c.ClusterCIDR, c.ServiceCIDR,
		c.CPUs, c.Memory, c.IngressPort, c.ProxyPort,
		c.RegistryEnabled, c.RegistryPort,
		strings.Join(c.CACertGlobs, ", "),
		strings.Join(c.EgressDomains, ", "),
		strings.Join(c.IngressDomains, ", "),
		len(c.Registries), c.BaseDir, c.ConfigFile)
}
