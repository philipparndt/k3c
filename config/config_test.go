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
	cmd := cfg.K3sCommand()
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
