package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseKernel(t *testing.T) {
	cases := []struct {
		in, fallback, want string
	}{
		{"", Kernel16K, Kernel16K},
		{"", Kernel4K, Kernel4K},
		{"16k", Kernel4K, Kernel16K},
		{"4k", Kernel16K, Kernel4K},
		{"keep", Kernel16K, KernelKeep},
		{"bundled", Kernel4K, Kernel16K},
		{"recommended", Kernel16K, Kernel4K},
		{" 4K ", Kernel16K, Kernel4K},
	}
	for _, c := range cases {
		got, err := parseKernel("cluster.kernel", c.in, c.fallback)
		if err != nil || got != c.want {
			t.Errorf("parseKernel(%q, fallback %q) = %q, %v; want %q", c.in, c.fallback, got, err, c.want)
		}
	}
	if _, err := parseKernel("docker.kernel", "default", Kernel16K); err == nil ||
		!strings.Contains(err.Error(), "docker.kernel") {
		t.Errorf("parseKernel(\"default\") error = %v, want an error naming docker.kernel", err)
	}
}

func resolveKernels(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "k3c.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("K3C_BASE_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("K3C_CONFIG", "")
	return Resolve("kernels", "")
}

func TestKernelResolution(t *testing.T) {
	cases := []struct {
		name, yaml           string
		wantNode, wantDocker string
	}{
		{"defaults: 16k clusters, 4k sidecar", "cluster:\n  name: kernels\n", Kernel16K, Kernel4K},
		{"sidecar does not follow cluster", "cluster:\n  name: kernels\n  kernel: keep\n", KernelKeep, Kernel4K},
		{"16k sidecar", "cluster:\n  name: kernels\ndocker:\n  kernel: 16k\n", Kernel16K, Kernel16K},
		{"everything 4k", "cluster:\n  name: kernels\n  kernel: 4k\n", Kernel4K, Kernel4K},
		{"legacy names", "cluster:\n  name: kernels\n  kernel: recommended\ndocker:\n  kernel: bundled\n", Kernel4K, Kernel16K},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := resolveKernels(t, c.yaml)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Kernel != c.wantNode || cfg.DockerKernel != c.wantDocker {
				t.Errorf("kernels = node %q, docker %q; want %q, %q", cfg.Kernel, cfg.DockerKernel, c.wantNode, c.wantDocker)
			}
		})
	}
}

func TestKernelResolutionRejectsUnknown(t *testing.T) {
	if _, err := resolveKernels(t, "cluster:\n  name: kernels\n  kernel: default\n"); err == nil ||
		!strings.Contains(err.Error(), "cluster.kernel") {
		t.Errorf("Resolve error = %v, want an error naming cluster.kernel", err)
	}
}
