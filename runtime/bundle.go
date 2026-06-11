package runtime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/philipparndt/go-logger"
)

// HasBundle reports whether a runtime payload is embedded in this binary.
// It is false for ordinary `go build` (no `bundled` tag, or no artifact).
func HasBundle() bool {
	return len(bundlePayload) > 0
}

// BundledContainerVersion returns the version of the embedded container
// runtime (written by `make bundle`), or "" when no bundle is embedded.
func BundledContainerVersion() string {
	if !HasBundle() {
		return ""
	}
	return strings.TrimSpace(bundledContainerVersion)
}

// bundleVersion identifies the embedded runtime payload: a filesystem-safe
// form of the bundled container version, naming the per-version cache
// directory so a changed bundle replaces a stale extraction.
func bundleVersion() string {
	v := BundledContainerVersion()
	if v == "" {
		v = "container-dev"
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		}
		return '-'
	}, v)
	if len(safe) > 64 {
		safe = safe[:64]
	}
	return safe
}

// cacheRoot is ~/.cache/k3c/runtime (honoring XDG_CACHE_HOME).
func cacheRoot() (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "k3c", "runtime"), nil
}

// extractBundle extracts the embedded runtime to
// ~/.cache/k3c/runtime/<version>/ exactly once. A `.complete` marker records
// a finished extraction so subsequent runs skip the work. Returns the
// extraction directory (the CONTAINER_INSTALL_ROOT).
func extractBundle() (string, error) {
	root, err := cacheRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, bundleVersion())
	marker := filepath.Join(dir, ".complete")

	if _, err := os.Stat(marker); err == nil {
		return dir, nil
	}

	// Stale or partial extraction: start clean.
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	logger.Info("extracting bundled container runtime to " + dir)
	if err := untarGz(bundlePayload, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("extracting bundled runtime: %w", err)
	}

	if err := os.WriteFile(marker, []byte(bundleVersion()+"\n"), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// untarGz extracts a gzip-compressed tar archive into dest. Paths are
// validated to stay within dest (zip-slip guard).
func untarGz(data []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()

	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, hdr.Name)
		// guard against path traversal
		if rel, err := filepath.Rel(destAbs, target); err != nil || rel == ".." || hasDotDotPrefix(rel) {
			return fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)&os.ModePerm|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFile(target, tr, os.FileMode(hdr.Mode)&os.ModePerm); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// skip other entry types (devices, fifos) — not expected here
			logger.Debug(fmt.Sprintf("skipping archive entry %s (type %d)", hdr.Name, hdr.Typeflag))
		}
	}
	return nil
}

func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && (rel[2] == filepath.Separator)
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Chmod(mode)
}
