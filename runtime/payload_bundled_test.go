//go:build bundled

package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// requiredPlugins are the container-runtime plugins the bundle MUST ship. A
// release that omits any of them breaks at runtime, not at build — most
// painfully container-network-gvnet, whose absence fails transparent-egress
// cluster/sidecar creation with "unable to locate network plugin
// container-network-gvnet" (shipped broken in v0.12.9–v0.12.11).
//
// This runs against the actually-embedded payload (go test -tags bundled), so a
// bundle missing a plugin fails the release build instead of a user's machine.
var requiredPlugins = []string{
	"container-runtime-linux",
	"container-network-vmnet",
	"container-network-gvnet",
	"container-core-images",
}

func TestBundledPayloadHasRequiredPlugins(t *testing.T) {
	if len(bundlePayload) == 0 {
		t.Fatal("embedded bundle payload is empty (built with -tags bundled but no payload?)")
	}

	gz, err := gzip.NewReader(bytes.NewReader(bundlePayload))
	if err != nil {
		t.Fatalf("gunzip payload: %v", err)
	}
	defer gz.Close()

	haveBin := map[string]bool{}
	haveCfg := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading payload tar: %v", err)
		}
		for _, p := range requiredPlugins {
			if strings.Contains(h.Name, "plugins/"+p+"/bin/"+p) {
				haveBin[p] = true
			}
			if strings.Contains(h.Name, "plugins/"+p+"/config.toml") {
				haveCfg[p] = true
			}
		}
	}

	for _, p := range requiredPlugins {
		if !haveBin[p] {
			t.Errorf("bundled runtime is missing plugin binary: %s", p)
		}
		if !haveCfg[p] {
			t.Errorf("bundled runtime is missing plugin config.toml: %s", p)
		}
	}
}

// requiredHelperBinaries are k3c's own helper binaries (built by `make bundle`,
// not part of the Apple `container` fork) that the runtime stages into guest
// VMs. They live at bin/ in the payload, distinct from the plugin tree checked
// above. A release missing one breaks at runtime, not at build — k3c-docker-fwd
// most painfully: without it the docker sidecar's engine is unreachable over the
// host socket and Testcontainers cannot start (it was silently absent from the
// v0.18.0/v0.19.0 bundles a user already had extracted).
var requiredHelperBinaries = []string{
	"bin/gvnet",
	"bin/k3c-docker-fwd",
}

func TestBundledPayloadHasHelperBinaries(t *testing.T) {
	if len(bundlePayload) == 0 {
		t.Fatal("embedded bundle payload is empty (built with -tags bundled but no payload?)")
	}

	gz, err := gzip.NewReader(bytes.NewReader(bundlePayload))
	if err != nil {
		t.Fatalf("gunzip payload: %v", err)
	}
	defer gz.Close()

	have := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading payload tar: %v", err)
		}
		name := strings.TrimPrefix(h.Name, "./")
		for _, b := range requiredHelperBinaries {
			if name == b || strings.HasSuffix(name, "/"+b) {
				have[b] = true
			}
		}
	}

	for _, b := range requiredHelperBinaries {
		if !have[b] {
			t.Errorf("bundled runtime is missing helper binary: %s", b)
		}
	}
}

// virtualizationEntitledPlugins are the plugins that drive
// Virtualization.framework and therefore MUST ship codesigned with the
// com.apple.security.virtualization entitlement. Without it they fail to launch,
// the apiserver hangs waiting on them at `container system start`, and every
// container call blocks — the bug that wedged cluster create on bundled
// installs (the fork's `make stage` once signed the installer pkg but not the
// staged tree k3c bundles).
var virtualizationEntitledPlugins = []string{
	"container-runtime-linux",
	"container-network-vmnet",
	"container-network-gvnet",
}

// TestBundledVMPluginsAreEntitled extracts the VM-touching plugin binaries from
// the embedded payload and asserts each carries the virtualization entitlement.
// Runs only on macOS, where codesign is available (the bundled release is
// macOS-only).
func TestBundledVMPluginsAreEntitled(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("codesign is only available on macOS")
	}
	if _, err := exec.LookPath("codesign"); err != nil {
		t.Skip("codesign not found")
	}
	if len(bundlePayload) == 0 {
		t.Fatal("embedded bundle payload is empty (built with -tags bundled but no payload?)")
	}

	gz, err := gzip.NewReader(bytes.NewReader(bundlePayload))
	if err != nil {
		t.Fatalf("gunzip payload: %v", err)
	}
	defer gz.Close()

	dir := t.TempDir()
	extracted := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading payload tar: %v", err)
		}
		for _, p := range virtualizationEntitledPlugins {
			if !strings.Contains(h.Name, "plugins/"+p+"/bin/"+p) {
				continue
			}
			out := filepath.Join(dir, p)
			f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY, 0o755)
			if err != nil {
				t.Fatalf("create %s: %v", out, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				t.Fatalf("extract %s: %v", p, err)
			}
			f.Close()
			extracted[p] = out
		}
	}

	const entitlement = "com.apple.security.virtualization"
	for _, p := range virtualizationEntitledPlugins {
		path, ok := extracted[p]
		if !ok {
			t.Errorf("plugin binary not found in payload: %s", p)
			continue
		}
		// `codesign -d --entitlements -` prints the binary's entitlements (in a
		// plist); a binary signed without them prints none.
		out, err := exec.Command("codesign", "-d", "--entitlements", "-", path).CombinedOutput()
		if err != nil {
			t.Errorf("codesign inspect %s: %v\n%s", p, err, out)
			continue
		}
		if !strings.Contains(string(out), entitlement) {
			t.Errorf("plugin %s is missing the %s entitlement; it will fail to "+
				"launch and hang container system start", p, entitlement)
		}
	}
}
