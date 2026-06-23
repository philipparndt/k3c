package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubeconfigPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	def := filepath.Join(home, ".kube", "config")
	sep := string(os.PathListSeparator)

	cases := []struct {
		name string
		env  string
		set  bool // whether KUBECONFIG is set at all
		want string
	}{
		{"unset falls back to ~/.kube/config", "", false, def},
		{"empty falls back to ~/.kube/config", "", true, def},
		{"single path is honoured", "/tmp/.dev/.kube/config", true, "/tmp/.dev/.kube/config"},
		{"first entry of a list wins", "/tmp/a/config" + sep + "/tmp/b/config", true, "/tmp/a/config"},
		{"leading empty entries are skipped", sep + sep + "/tmp/c/config", true, "/tmp/c/config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.set {
				t.Setenv("KUBECONFIG", c.env)
			} else {
				os.Unsetenv("KUBECONFIG")
			}
			got, err := kubeconfigPath()
			if err != nil {
				t.Fatalf("kubeconfigPath() error: %v", err)
			}
			if got != c.want {
				t.Errorf("kubeconfigPath() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestKubeconfigPathHonoursEnv guards the specific regression that made
// `cluster create` fail: the merge wrote ~/.kube/config while use-context
// read the ambient KUBECONFIG, so a KUBECONFIG pointing elsewhere broke the
// merge. The resolver must point both at the same file.
func TestKubeconfigPathHonoursEnv(t *testing.T) {
	t.Setenv("KUBECONFIG", "/some/other/.kube/config")
	got, err := kubeconfigPath()
	if err != nil {
		t.Fatalf("kubeconfigPath() error: %v", err)
	}
	if got != "/some/other/.kube/config" {
		t.Errorf("kubeconfigPath() = %q, want it to honour KUBECONFIG", got)
	}
	if strings.HasSuffix(got, filepath.Join(".kube", "config")) && strings.Contains(got, "/some/other/") == false {
		t.Errorf("resolver ignored KUBECONFIG and fell back to default")
	}
}
