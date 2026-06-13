package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestK3sCommandContainsSysctls(t *testing.T) {
	cfg, err := Resolve("test", "/nonexistent-k3c.yaml")
	if err == nil {
		t.Skip("unexpected config load")
	}
	cfg, err = Resolve("test", "")
	if err != nil {
		t.Fatal(err)
	}
	cmd := cfg.K3sCommand(false)
	for _, want := range []string{
		"sysctl -w fs.inotify.max_user_instances=1024",
		"sysctl -w fs.inotify.max_user_watches=1048576",
		"exec k3s server",
		"xtables-legacy-multi",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("k3s command missing %q:\n%s", want, cmd)
		}
	}

	// On the old kernel the workarounds are present; on the modern kernel
	// (br_netfilter + vxlan) they must be gone.
	old := cfg.K3sCommand(false)
	if !strings.Contains(old, "--flannel-backend=host-gw") || !strings.Contains(old, "masquerade-all") {
		t.Errorf("old-kernel command missing workarounds:\n%s", old)
	}
	modern := cfg.K3sCommand(true)
	if strings.Contains(modern, "host-gw") || strings.Contains(modern, "masquerade-all") {
		t.Errorf("modern-kernel command should not have workarounds:\n%s", modern)
	}
}

// A project k3c.yaml found in the working directory must only apply to its
// own cluster: resolving a different named cluster from that directory must
// not inherit (or later overwrite) the project's settings.
func TestProjectConfigForeignCluster(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "k3c.yaml"),
		[]byte("cluster:\n  name: vehub\n  contextPrefix: k3d-\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("K3C_BASE_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("K3C_CONFIG", "")

	foreign, err := Resolve("k3c", "")
	if err != nil {
		t.Fatal(err)
	}
	if foreign.KubeContext != "k3c-k3c" || foreign.ConfigFile != "" {
		t.Errorf("foreign cluster inherited project config: context=%s configFile=%s",
			foreign.KubeContext, foreign.ConfigFile)
	}

	own, err := Resolve("vehub", "")
	if err != nil {
		t.Fatal(err)
	}
	if own.KubeContext != "k3d-vehub" {
		t.Errorf("own cluster did not pick up project config: context=%s", own.KubeContext)
	}

	def, err := Resolve("", "")
	if err != nil {
		t.Fatal(err)
	}
	if def.Cluster != "vehub" {
		t.Errorf("default resolution did not use the project cluster: %s", def.Cluster)
	}
}

func TestCorednsCustomCatchAll(t *testing.T) {
	cfg := &Config{
		EgressDomains: []string{"*"},
		VmnetGateway:  "192.168.64.1",
		ServerName:    "demo-server",
	}
	manifest := cfg.CorednsCustom()
	for _, want := range []string{
		"template IN ANY cluster.local in-addr.arpa ip6.arpa demo-server",
		"fallthrough",
		"template IN A .",
		`answer "{{ .Name }} 60 IN A 192.168.64.1"`,
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("catch-all manifest is missing %q:\n%s", want, manifest)
		}
	}

	cfg.EgressDomains = []string{"vector.com", "vector.int"}
	manifest = cfg.CorednsCustom()
	if !strings.Contains(manifest, "template IN A vector.com vector.int {") {
		t.Errorf("domain-list manifest changed:\n%s", manifest)
	}
	if strings.Contains(manifest, "fallthrough") {
		t.Errorf("domain-list manifest must not contain the catch-all guard:\n%s", manifest)
	}
}

func TestParseForwards(t *testing.T) {
	fwds, err := parseForwards([]string{"9480:gateway.zscloud.net:9480"})
	if err != nil || len(fwds) != 1 || fwds[0].Port != "9480" || fwds[0].Target != "gateway.zscloud.net:9480" {
		t.Fatalf("unexpected result: %v, %v", fwds, err)
	}
	for _, bad := range []string{"9480", "x:host:1", "9480:hostonly"} {
		if _, err := parseForwards([]string{bad}); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestEffectiveRegistries(t *testing.T) {
	cfg := &Config{
		VmnetGateway:     "192.168.64.1",
		PullCachePort:    "5011",
		PullCacheEnabled: true,
		Registries: `mirrors:
  "docker.io":
    endpoint:
      - https://dockerhub-remote.example.com
  "registry.local:5001":
    endpoint:
      - http://192.168.64.1:5001
configs:
  "private-reg.example.com":
    tls:
      ca_file: /etc/rancher/k3s/ca-bundle.pem
`,
	}
	out := cfg.EffectiveRegistries()
	for _, want := range []string{
		"http://192.168.64.1:5011",                // the cache endpoint
		"https://dockerhub-remote.example.com",    // original upstream as fallback
		"https://private-reg.example.com",         // configs-only host gets a mirror
		"ca_file: /etc/rancher/k3s/ca-bundle.pem", // configs preserved
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rewritten registries missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "192.168.64.1:5011") != 2 {
		t.Errorf("cache endpoint should front exactly docker.io and the private registry:\n%s", out)
	}
	ups := RegistryUpstreams(cfg.Registries)
	if got := ups["docker.io"][0]; got != "https://dockerhub-remote.example.com" {
		t.Errorf("upstream mapping wrong: %v", ups)
	}

	cfg.PullCacheEnabled = false
	if cfg.EffectiveRegistries() != cfg.Registries {
		t.Error("disabled pull cache must keep registries verbatim")
	}
}
