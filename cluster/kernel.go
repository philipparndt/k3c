package cluster

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/philipparndt/go-logger"

	"k3c/config"
	k3cruntime "k3c/runtime"
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

// EnsureClusterKernel manages the default guest kernel per the cluster
// config before a VM is created. "bundled" (the default) installs the
// 16K-page kernel shipped with k3c: on Apple silicon the hypervisor frees
// ballooned guest memory at host (16K) page granularity, so only a
// 16K-page guest returns memory reliably — a fragmented 4K guest barely
// returns anything. The trade-off is Rosetta: amd64 images cannot run on a
// 16K kernel, so "recommended" selects the runtime's 4K kata kernel for
// clusters that need them, and "keep" leaves the kernel alone entirely.
// Existing VMs keep their kernel; only newly created ones pick up changes.
func EnsureClusterKernel(cfg *config.Config) {
	switch cfg.Kernel {
	case "keep":
		return
	case "recommended":
		ensureRosettaKernel()
		return
	}
	bundled := k3cruntime.BundledKernel()
	if bundled == "" {
		// unbundled dev build: fall back to the previous behavior
		EnsureRecommendedKernel()
		return
	}
	if kernelFileMatches(bundled) {
		return
	}
	logger.Info("installing the bundled 16K kernel (best host memory return; set cluster.kernel: recommended for amd64/Rosetta workloads)")
	if out, err := runContainer("system", "kernel", "set", "--binary", bundled, "--force"); err != nil {
		logger.Warn("could not install the bundled kernel: " + firstLine(out))
		EnsureRecommendedKernel()
		return
	}
	if !KernelHasModernNetfilter() {
		logger.Warn("bundled kernel lacks br_netfilter/vxlan?! falling back to the recommended kernel")
		_, _ = runContainer("system", "kernel", "set", "--recommended")
	}
}

// kernelFileMatches reports whether the default container kernel is the
// given file (by content hash).
func kernelFileMatches(path string) bool {
	want, err := fileSHA256(path)
	if err != nil {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	current, err := fileSHA256(filepath.Join(home, "Library", "Application Support",
		"com.apple.container", "kernels", "default.kernel-"+runtime.GOARCH))
	if err != nil {
		return false
	}
	return want == current
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// ensureRosettaKernel implements cluster.kernel: recommended. Installing the
// recommended kernel only when br_netfilter/vxlan are missing is not enough
// here: the bundled 16K kernel has both, so a user switching from "bundled"
// would silently stay on 16K pages, where Rosetta fails every amd64 binary
// with "unable to mmap ELF: 22". So the recommended kernel is also installed
// when the default kernel's embedded config shows a page size other than 4K.
// An unreadable config falls back to the netfilter check alone.
func ensureRosettaKernel() {
	cfg, err := defaultKernelConfig()
	if err != nil {
		EnsureRecommendedKernel()
		return
	}
	if kb := kernelPageSizeKB(cfg); kb != 0 && kb != 4 {
		logger.Info(fmt.Sprintf("default kernel uses %dK pages; Rosetta/amd64 needs 4K — installing the recommended kernel (container system kernel set --recommended)", kb))
		if out, err := runContainer("system", "kernel", "set", "--recommended"); err != nil {
			logger.Warn("could not install the recommended kernel: " + firstLine(out) + " — amd64 images will not run")
			return
		}
		logger.Info("recommended 4K kernel installed; existing VMs keep their kernel until re-created (e.g. k3c docker up --recreate)")
	}
	EnsureRecommendedKernel()
}

// kernelPageSizeKB returns the arm64 page size in KiB from a kernel .config
// (CONFIG_ARM64_{4K,16K,64K}_PAGES=y), or 0 when none is set.
func kernelPageSizeKB(cfg []byte) int {
	for _, kb := range []int{4, 16, 64} {
		if bytes.Contains(cfg, []byte(fmt.Sprintf("\nCONFIG_ARM64_%dK_PAGES=y\n", kb))) {
			return kb
		}
	}
	return 0
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
