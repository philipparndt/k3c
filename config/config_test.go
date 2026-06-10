package config

import (
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
