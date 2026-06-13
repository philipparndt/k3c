package cluster

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Kernel capability detection. The Apple `container` default kernel is a Kata
// Containers build; the older vmlinux-6.12.28-153 is compiled WITHOUT
// CONFIG_BRIDGE_NETFILTER and CONFIG_VXLAN, forcing k3s workarounds (host-gw
// flannel + kube-proxy masquerade-all). The recommended vmlinux-6.18.15-186
// has both. We detect this from the kernel's embedded .config — kata kernels
// set CONFIG_IKCONFIG=y, so the gzipped config is baked into the image between
// the IKCFG_ST/IKCFG_ED markers — so no probe VM has to be booted.

var (
	ikcfgStart = []byte("IKCFG_ST")
	ikcfgEnd   = []byte("IKCFG_ED")

	modernKernelOnce sync.Once
	modernKernelVal  bool
)

// KernelHasModernNetfilter reports whether the default container kernel has
// br_netfilter and vxlan built in. On any error (kernel file not found, no
// embedded config) it returns false, so the k3s workarounds stay enabled —
// the safe choice for the old kernel.
func KernelHasModernNetfilter() bool {
	modernKernelOnce.Do(func() {
		cfg, err := defaultKernelConfig()
		if err != nil {
			return
		}
		modernKernelVal = bytes.Contains(cfg, []byte("CONFIG_BRIDGE_NETFILTER=y")) &&
			bytes.Contains(cfg, []byte("CONFIG_VXLAN=y"))
	})
	return modernKernelVal
}

// defaultKernelConfig extracts the gzipped .config embedded in the default
// container kernel image (CONFIG_IKCONFIG).
func defaultKernelConfig() ([]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, "Library", "Application Support",
		"com.apple.container", "kernels", "default.kernel-"+runtime.GOARCH)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := bytes.Index(data, ikcfgStart)
	e := bytes.Index(data, ikcfgEnd)
	if s < 0 || e < 0 || e <= s {
		return nil, fmt.Errorf("no embedded kernel config (CONFIG_IKCONFIG) in %s", path)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data[s+len(ikcfgStart) : e]))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}
