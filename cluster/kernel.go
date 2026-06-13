package cluster

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/philipparndt/go-logger"
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
)

// KernelHasModernNetfilter reports whether the default container kernel has
// br_netfilter and vxlan built in. On any error (kernel file not found, no
// embedded config) it returns false, so the k3s workarounds stay enabled —
// the safe choice for the old kernel. Not cached: the default kernel can
// change at runtime (see EnsureRecommendedKernel), and it is read at most a
// couple of times per cluster create.
func KernelHasModernNetfilter() bool {
	cfg, err := defaultKernelConfig()
	if err != nil {
		return false
	}
	return bytes.Contains(cfg, []byte("CONFIG_BRIDGE_NETFILTER=y")) &&
		bytes.Contains(cfg, []byte("CONFIG_VXLAN=y"))
}

// EnsureRecommendedKernel upgrades the default container kernel to the
// recommended one when the current default lacks br_netfilter/vxlan, so new
// clusters (and the docker sidecar) run on a capable kernel and skip the
// host-gw/masquerade-all workarounds. It is a no-op when the kernel is already
// modern, and degrades gracefully (keeping the workarounds) if the upgrade
// fails — e.g. no network to fetch the kernel. Existing VMs keep their kernel;
// only newly created ones pick up the change.
func EnsureRecommendedKernel() {
	if KernelHasModernNetfilter() {
		return
	}
	logger.Info("default kernel lacks br_netfilter/vxlan; installing the recommended kernel (container system kernel set --recommended)")
	if out, err := runContainer("system", "kernel", "set", "--recommended"); err != nil {
		logger.Warn("could not install the recommended kernel: " + firstLine(out) + " — new clusters will use the host-gw/masquerade-all workarounds")
		return
	}
	if KernelHasModernNetfilter() {
		logger.Info("recommended kernel installed (has br_netfilter + vxlan); new clusters run without workarounds")
	}
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
