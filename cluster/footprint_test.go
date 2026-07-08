package cluster

import "testing"

// Real footprint(1) shapes: the header's "Footprint:" counts dirty pages
// only, while the auxiliary phys_footprint counter also charges pages the
// balloon returned to macOS as reclaimable (seen after warm-snapshot
// suspend/resume). vmRAM must prefer the header.
const footprintSample = `======================================================================
com.apple.Virtualization.VirtualMachine [42934]: 64-bit    Footprint: 16 GB (16384 bytes per page)
======================================================================

  Dirty      Clean  Reclaimable    Regions    Category
    ---        ---          ---        ---    ---
  16 GB        0 B      9473 MB        278    untagged (VM_ALLOCATE)

Auxiliary data:
    phys_footprint: 28 GB
    phys_footprint_peak: 40 GB
`

func TestScanFootprintPrefersDirtyHeader(t *testing.T) {
	b, ok := scanFootprint(footprintSample, "Footprint:")
	if !ok || b != 16<<30 {
		t.Fatalf("header Footprint = %d, %v; want %d", b, ok, int64(16)<<30)
	}
	// phys_footprint stays available as the fallback and must not match
	// phys_footprint_peak first.
	b, ok = scanFootprint(footprintSample, "phys_footprint:")
	if !ok || b != 28<<30 {
		t.Fatalf("phys_footprint = %d, %v; want %d", b, ok, int64(28)<<30)
	}
}

func TestScanFootprintMissingMarker(t *testing.T) {
	if _, ok := scanFootprint("no such line\n", "Footprint:"); ok {
		t.Fatal("expected no match")
	}
}
