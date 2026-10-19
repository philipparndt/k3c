package cluster

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
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

// KernelHasModernNetfilter reports whether the given kernel image ("" = the
// default container kernel) has br_netfilter and vxlan built in. On any error
// (kernel file not found, no embedded config) it returns false, so the k3s
// workarounds stay enabled — the safe choice for the old kernel. Not cached:
// the default kernel can change at runtime (see EnsureRecommendedKernel), and
// it is read at most a couple of times per cluster create.
func KernelHasModernNetfilter(kernel string) bool {
	cfg, err := kernelConfig(kernel)
	if err != nil {
		return false
	}
	return bytes.Contains(cfg, []byte("CONFIG_BRIDGE_NETFILTER=y")) &&
		bytes.Contains(cfg, []byte("CONFIG_VXLAN=y"))
}

// Guest kernels. On Apple silicon the hypervisor frees ballooned guest memory
// at host (16K) page granularity, so only a 16K-page guest returns memory
// reliably — a fragmented 4K guest barely returns anything. Rosetta, however,
// needs 4K pages: on a 16K kernel every amd64 binary fails with "rosetta
// error: unable to mmap ELF: 22". k3c therefore picks the kernel per VM
// (`container run --kernel`) instead of switching the machine-wide default:
//
//   - "16k" (default): the 16K-page kernel shipped with k3c
//   - "4k": the runtime's recommended 4K kata kernel (amd64/Rosetta works)
//   - "keep": no --kernel, the VM boots whatever the default kernel is
//
// cluster.kernel selects the kernel of the cluster node VM (default 16k),
// docker.kernel the one of the docker sidecar (default 4k: amd64 image builds
// are its common case), so the sidecar runs amd64 while clusters keep the 16K
// kernel. A VM keeps the kernel it was created with; a change applies when it
// is re-created.

// vmKernel resolves the kernel file a new VM boots for the given mode, or ""
// when the VM should boot the default kernel.
func vmKernel(mode string) (string, error) {
	switch mode {
	case config.KernelKeep:
		return "", nil
	case config.Kernel4K:
		return recommendedKernelFile()
	}
	bundled := k3cruntime.BundledKernel()
	if bundled == "" {
		// unbundled dev build: no 16K kernel to pass; boot the default kernel
		// (EnsureClusterKernel keeps it modern)
		logger.Debug("no bundled 16K kernel (unbundled build); booting the default kernel")
	}
	return bundled, nil
}

// kernelArgs returns the `container run` arguments selecting the kernel.
func kernelArgs(kernel string) []string {
	if kernel == "" {
		return nil
	}
	return []string{"--kernel", kernel}
}

// EnsureClusterKernel manages the machine-wide default kernel, which only VMs
// k3c creates without an explicit kernel (and plain `k3c container run`) boot.
// With cluster.kernel 16k it installs the bundled 16K kernel as the default so
// those VMs return memory well too; with 4k and keep the default is left
// alone, since k3c's own VMs get their kernel per VM (see vmKernel).
func EnsureClusterKernel(cfg *config.Config) {
	if cfg.Kernel != config.Kernel16K {
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
	logger.Info("installing the bundled 16K kernel as the default (best host memory return; set cluster.kernel or docker.kernel: 4k for amd64/Rosetta workloads)")
	if out, err := runContainer("system", "kernel", "set", "--binary", bundled, "--force"); err != nil {
		logger.Warn("could not install the bundled kernel: " + firstLine(out))
		EnsureRecommendedKernel()
		return
	}
	if !KernelHasModernNetfilter("") {
		logger.Warn("bundled kernel lacks br_netfilter/vxlan?! falling back to the recommended kernel")
		_, _ = runContainer("system", "kernel", "set", "--recommended")
	}
}

// recommendedKernelFile returns the runtime's recommended (4K kata) kernel
// file, fetching it once if it is not installed yet. The runtime downloads and
// verifies it (`container system kernel set --recommended`), but that also
// makes it the machine-wide default; the previous default is restored, so
// selecting 4k for one VM never changes the kernel of the others.
func recommendedKernelFile() (string, error) {
	out, err := runContainer("system", "property", "list", "--format", "json")
	if err != nil {
		return "", fmt.Errorf("reading the runtime's recommended kernel: %s", firstLine(out))
	}
	name, err := recommendedKernelName([]byte(out))
	if err != nil {
		return "", err
	}
	file := filepath.Join(kernelsDir(), name)
	if _, err := os.Stat(file); err != nil {
		prev, _ := os.Readlink(defaultKernelPath())
		logger.Info("fetching the runtime's recommended 4K kernel " + name + " (one-time)")
		if out, err := runContainer("system", "kernel", "set", "--recommended"); err != nil {
			return "", fmt.Errorf("fetching the recommended 4K kernel: %s", firstLine(out))
		}
		if prev != "" {
			if err := relinkDefaultKernel(prev); err != nil {
				logger.Warn("could not restore the default kernel " + prev + ": " + err.Error())
			}
		}
		if _, err := os.Stat(file); err != nil {
			return "", fmt.Errorf("recommended kernel %s not found after installing it", file)
		}
	}
	if cfg, err := kernelConfig(file); err == nil {
		if kb := kernelPageSizeKB(cfg); kb != 0 && kb != 4 {
			logger.Warn(fmt.Sprintf("the runtime's recommended kernel %s uses %dK pages; amd64/Rosetta needs 4K", name, kb))
		}
	}
	return file, nil
}

// recommendedKernelName extracts the file name of the recommended kernel from
// `container system property list --format json` (kernel.binaryPath, a path
// inside the kata tarball; the runtime installs it under its base name).
func recommendedKernelName(props []byte) (string, error) {
	var p struct {
		Kernel struct {
			BinaryPath string `json:"binaryPath"`
		} `json:"kernel"`
	}
	if err := json.Unmarshal(props, &p); err != nil {
		return "", fmt.Errorf("parsing the runtime properties: %w", err)
	}
	name := path.Base(p.Kernel.BinaryPath)
	if p.Kernel.BinaryPath == "" || name == "." || name == "/" {
		return "", fmt.Errorf("the runtime defines no recommended kernel (kernel.binaryPath)")
	}
	return name, nil
}

// relinkDefaultKernel points the default kernel at target again, atomically
// (a temporary symlink renamed over the old one). `kernel set --binary` cannot
// be used for a file already in the kernels directory: it would copy the file
// onto itself.
func relinkDefaultKernel(target string) error {
	link := defaultKernelPath()
	tmp := link + ".k3c-tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
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

func kernelsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "com.apple.container", "kernels")
}

func defaultKernelPath() string {
	return filepath.Join(kernelsDir(), "default.kernel-"+runtime.GOARCH)
}

// kernelFileMatches reports whether the default container kernel is the
// given file (by content hash).
func kernelFileMatches(path string) bool {
	want, err := fileSHA256(path)
	if err != nil {
		return false
	}
	current, err := fileSHA256(defaultKernelPath())
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

// EnsureRecommendedKernel upgrades the default container kernel to the
// recommended one when the current default lacks br_netfilter/vxlan, so new
// clusters (and the docker sidecar) run on a capable kernel and skip the
// host-gw/masquerade-all workarounds. It is a no-op when the kernel is already
// modern, and degrades gracefully (keeping the workarounds) if the upgrade
// fails — e.g. no network to fetch the kernel. Existing VMs keep their kernel;
// only newly created ones pick up the change.
func EnsureRecommendedKernel() {
	if KernelHasModernNetfilter("") {
		return
	}
	logger.Info("default kernel lacks br_netfilter/vxlan; installing the recommended kernel (container system kernel set --recommended)")
	if out, err := runContainer("system", "kernel", "set", "--recommended"); err != nil {
		logger.Warn("could not install the recommended kernel: " + firstLine(out) + " — new clusters will use the host-gw/masquerade-all workarounds")
		return
	}
	if KernelHasModernNetfilter("") {
		logger.Info("recommended kernel installed (has br_netfilter + vxlan); new clusters run without workarounds")
	}
}

// kernelConfig extracts the gzipped .config embedded in a kernel image
// (CONFIG_IKCONFIG); "" reads the default container kernel.
func kernelConfig(kernel string) ([]byte, error) {
	if kernel == "" {
		kernel = defaultKernelPath()
	}
	data, err := os.ReadFile(kernel)
	if err != nil {
		return nil, err
	}
	s := bytes.Index(data, ikcfgStart)
	e := bytes.Index(data, ikcfgEnd)
	if s < 0 || e < 0 || e <= s {
		return nil, fmt.Errorf("no embedded kernel config (CONFIG_IKCONFIG) in %s", kernel)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data[s+len(ikcfgStart) : e]))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}
