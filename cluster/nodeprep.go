package cluster

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/philipparndt/go-logger"

	"k3c/config"
	"k3c/runtime"
)

// Nested k3d node-image preparation.
//
// A team's existing `k3d cluster create --config <file>` flow (the same k3d
// config they use on Linux) runs unchanged on k3c: with the docker sidecar up,
// k3d creates the node as a container nested inside the sidecar VM. That nesting
// breaks two assumptions, which we fix here transparently (no edit to the k3d
// config):
//
//  1. Corporate CA trust. A k3d config whose mirrors point at corporate HTTPS
//     registries with no explicit ca_file relies on the node's system trust. The
//     usual Linux trick injects the CA with `--volume cert:/etc/ssl/certs/
//     <name>@all`, but that host path does not exist inside the sidecar VM, so
//     docker bind-mounts an empty directory and the CA never lands — pulls then
//     fail with "x509: certificate signed by unknown authority". We instead
//     bake the configured caCerts into the node image's CA bundle.
//
//  2. Native architecture. An emulated amd64 k3s node on Apple `container`
//     breaks containerd's seccomp detection (every pod sandbox fails with
//     "seccomp is not supported"); the same image as the host's native arch
//     works. We rebuild at the sidecar's architecture so the node runs native
//     (amd64 workload images still run on it via the sidecar's Rosetta binfmt).
//
// The prepared image is tagged identically, so k3d's `image:` reference uses it
// without a config change (k3d does not re-pull an image already present).

const caInjectLabel = "k3c.ca-injected"

// PrepareK3sNodeImages bakes the corporate CA into each configured k3s node
// image at the sidecar's native architecture. It is a no-op without configured
// node images or CA certs, and skips images already prepared with the current
// CA (idempotent across `docker up` runs). The docker sidecar must be running.
func PrepareK3sNodeImages(cfg *config.Config) error {
	if len(cfg.DockerK3sNodeImages) == 0 {
		logger.Info("no k3d node images configured (docker.k3sNodeImages); nothing to prepare")
		return nil
	}
	if len(cfg.CACertGlobs) == 0 {
		logger.Info("no corporate CA configured (caCerts); nothing to prepare")
		return nil
	}
	ca, err := corpCACerts(cfg)
	if err != nil {
		return err
	}
	if len(ca) == 0 {
		return nil
	}
	sum := sha256.Sum256(ca)
	hash := hex.EncodeToString(sum[:])[:16]
	arch := sidecarArch()
	for _, img := range cfg.DockerK3sNodeImages {
		if nodeImagePrepared(img, hash) {
			logger.Info("k3d node image already prepared: " + img)
			continue
		}
		logger.Info(fmt.Sprintf("preparing k3d node image %s (corporate CA, %s)", img, arch))
		if err := bakeNodeImage(img, ca, arch, hash); err != nil {
			return fmt.Errorf("preparing node image %s: %w", img, err)
		}
	}
	return nil
}

// corpCACerts concatenates the configured CA certificates (the caCerts globs),
// without the host system bundle — only the extra roots the node must trust.
func corpCACerts(cfg *config.Config) ([]byte, error) {
	var bundle []byte
	// CAs the host trusts (macOS System keychain) — e.g. a corporate root or an
	// internal registry CA — must be baked into node/builder images too, so a
	// nested build pulling base images through the corporate-CA registry mirror
	// verifies. This does not depend on caCerts globs and pins no specific CA.
	if extra := systemKeychainCerts(); len(extra) > 0 {
		bundle = append(bundle, extra...)
		bundle = append(bundle, '\n')
	}
	for _, glob := range cfg.CACertGlobs {
		matches, err := filepath.Glob(glob)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			// A configured CA glob that matches nothing is normal (no corporate
			// CAs, or per-cluster certs not generated yet). The system bundle is
			// always trusted; corporate CAs are additive, so skip, don't fail.
			logger.Warn("no CA certificates match " + glob + " — skipping")
			continue
		}
		for _, crt := range matches {
			data, err := os.ReadFile(crt)
			if err != nil {
				return nil, err
			}
			bundle = append(bundle, data...)
			bundle = append(bundle, '\n')
		}
	}
	return bundle, nil
}

// sidecarArch returns the sidecar engine's architecture (e.g. "arm64"),
// defaulting to arm64 (Apple silicon) when it cannot be determined.
func sidecarArch() string {
	out, err := runContainer("exec", dockerName, "docker", "version", "--format", "{{.Server.Arch}}")
	if err == nil {
		// The container CLI can prepend noise (a debug build prints
		// "Warning! Running debug build…"), so don't assume the first line is the
		// arch. The arch is a single bare token; take the last arch-shaped line
		// (whitespace-free) and ignore warnings.
		lines := strings.Split(out, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			s := strings.TrimSpace(lines[i])
			if archToken.MatchString(s) {
				return s
			}
		}
	}
	return "arm64"
}

// archToken matches a platform arch component (e.g. arm64, amd64, aarch64) and
// nothing containing spaces, so warning lines from the container CLI are skipped.
var archToken = regexp.MustCompile(`^[a-z0-9_]+$`)

// nodeImagePrepared reports whether img already carries our CA-injected label
// for the current CA bundle hash.
func nodeImagePrepared(img, hash string) bool {
	out, err := runContainer("exec", dockerName, "docker", "image", "inspect", img,
		"--format", "{{ index .Config.Labels \""+caInjectLabel+"\" }}")
	return err == nil && strings.TrimSpace(firstLine(out)) == hash
}

// bakeNodeImage rebuilds img at arch with the CA appended to the node's CA
// bundle, retagging it in place. The build context (Dockerfile + CA bundle) is
// streamed to the sidecar's docker engine over stdin, so it needs no files
// inside the VM.
func bakeNodeImage(img string, ca []byte, arch, hash string) error {
	dockerfile := "FROM " + img + "\n" +
		"COPY k3c-corp-ca.crt /usr/local/share/ca-certificates/k3c-corp-ca.crt\n" +
		"RUN cat /usr/local/share/ca-certificates/k3c-corp-ca.crt >> /etc/ssl/certs/ca-certificates.crt\n" +
		"LABEL " + caInjectLabel + "=" + hash + "\n"

	ctx, err := buildContext(map[string][]byte{
		"Dockerfile":      []byte(dockerfile),
		"k3c-corp-ca.crt": ca,
	})
	if err != nil {
		return err
	}
	// --provenance=false keeps the result a plain single-arch image (not a
	// manifest list with an attestation), which k3d's image-arch detection and
	// `docker run` resolve cleanly.
	cmd := runtime.Command("exec", "-i", dockerName, "docker", "build",
		"--platform", "linux/"+arch, "--provenance=false", "-t", img, "-")
	cmd.Stdin = bytes.NewReader(ctx)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker build: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// buildContext builds an uncompressed tar of the given files (a docker build
// context to feed `docker build -`).
func buildContext(files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(data)),
		}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
