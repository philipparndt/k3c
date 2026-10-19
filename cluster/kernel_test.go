package cluster

import "testing"

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
