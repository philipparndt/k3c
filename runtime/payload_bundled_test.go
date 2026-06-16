//go:build bundled

package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
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
