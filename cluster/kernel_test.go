package cluster

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestKernelPageSizeKB(t *testing.T) {
	cases := []struct {
		name string
		cfg  string
		want int
	}{
		{"bundled 16K kernel", "#\n# Linux/arm64 6.18.0 Kernel Configuration\n#\nCONFIG_ARM64_16K_PAGES=y\nCONFIG_BRIDGE_NETFILTER=y\nCONFIG_VXLAN=y\n", 16},
		{"kata 4K kernel", "#\n# Linux/arm64 6.18.35 Kernel Configuration\n#\nCONFIG_ARM64_4K_PAGES=y\nCONFIG_BRIDGE_NETFILTER=y\n", 4},
		{"64K kernel", "#\nCONFIG_ARM64_64K_PAGES=y\n", 64},
		{"unset options are ignored", "#\n# CONFIG_ARM64_4K_PAGES is not set\nCONFIG_ARM64_16K_PAGES=y\n", 16},
		{"no page size option", "#\nCONFIG_VXLAN=y\n", 0},
		{"no prefix match", "#\nCONFIG_ARM64_4K_PAGES_EXTRA=y\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := kernelPageSizeKB([]byte(c.cfg)); got != c.want {
				t.Errorf("kernelPageSizeKB() = %d, want %d", got, c.want)
			}
		})
	}
}

func TestRecommendedKernelName(t *testing.T) {
	props := `{"build":{"cpus":2},"kernel":{"binaryPath":"opt\/kata\/share\/kata-containers\/vmlinux-6.18.35-197-debug","digest":"sha256:8736","url":"https:\/\/example.invalid\/kata-static-3.32.0-arm64.tar.zst"}}`
	got, err := recommendedKernelName([]byte(props))
	if err != nil || got != "vmlinux-6.18.35-197-debug" {
		t.Errorf("recommendedKernelName() = %q, %v; want vmlinux-6.18.35-197-debug", got, err)
	}
	for _, bad := range []string{`{"kernel":{}}`, `{}`, `not json`} {
		if got, err := recommendedKernelName([]byte(bad)); err == nil {
			t.Errorf("recommendedKernelName(%s) = %q, want an error", bad, got)
		}
	}
}

func TestKernelArgs(t *testing.T) {
	if got := kernelArgs(""); got != nil {
		t.Errorf("kernelArgs(\"\") = %v, want none (boot the default kernel)", got)
	}
	want := []string{"--kernel", "/k/vmlinux-16k"}
	if got := kernelArgs("/k/vmlinux-16k"); !reflect.DeepEqual(got, want) {
		t.Errorf("kernelArgs() = %v, want %v", got, want)
	}
}

func TestRelinkDefaultKernel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := kernelsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	k16 := filepath.Join(dir, "vmlinux-16k")
	k4 := filepath.Join(dir, "vmlinux-4k")
	for _, f := range []string{k16, k4} {
		if err := os.WriteFile(f, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// the runtime's `kernel set --recommended` repointed the default at 4K
	if err := os.Symlink(k4, defaultKernelPath()); err != nil {
		t.Fatal(err)
	}
	if err := relinkDefaultKernel(k16); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(defaultKernelPath()); err != nil || got != k16 {
		t.Errorf("default kernel -> %q, %v; want %q", got, err, k16)
	}
	// both kernel files are untouched
	for _, f := range []string{k16, k4} {
		if b, err := os.ReadFile(f); err != nil || string(b) != f {
			t.Errorf("%s changed: %q, %v", f, b, err)
		}
	}
}
