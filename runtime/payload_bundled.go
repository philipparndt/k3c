//go:build bundled

package runtime

import _ "embed"

// bundlePayload is the embedded container runtime tree (a gzip-compressed
// tar of the install root: bin/, libexec/, and the init image init.tar).
// It is staged by `make bundle` and only compiled into release builds
// (`go build -tags bundled`). The artifact is large (~300MB) and is NOT
// committed to git — see runtime/payload/README.md.
//
//go:embed payload/container-runtime.tar.gz
var bundlePayload []byte

// bundledContainerVersion is the version of the embedded container runtime,
// written next to the payload by `make bundle` and shown by `k3c version`.
//
//go:embed payload/container-version.txt
var bundledContainerVersion string
