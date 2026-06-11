//go:build !bundled

package runtime

// bundlePayload is empty in ordinary builds: no runtime is embedded, so k3c
// drives a host-installed `container`. Release builds use `-tags bundled`
// (after `make bundle` stages the artifact) to embed the real payload.
var bundlePayload []byte

// bundledContainerVersion is empty in ordinary builds.
var bundledContainerVersion string
