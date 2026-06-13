// Package runtime resolves which Apple `container` CLI k3c invokes and with
// what environment.
//
// k3c can either drive a `container` installed on the host, or a runtime
// bundled into the k3c binary itself (release builds, built with the
// `bundled` build tag). The bundled tree is extracted on first use into a
// per-version cache directory and driven via CONTAINER_INSTALL_ROOT.
//
// Resolution precedence (highest first):
//
//  1. K3C_CONTAINER_BINARY — explicit path/name of the container CLI.
//  2. K3C_CONTAINER_FROM_PATH truthy — use `container` from PATH.
//  3. A user-configured containerBinary (config), if explicitly set.
//  4. An embedded bundled runtime — extracted to the cache and used with
//     CONTAINER_INSTALL_ROOT pointing at the extraction dir.
//  5. Fallback: `container` from PATH.
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/philipparndt/go-logger"
)

// configuredBinary is the user-configured container CLI path, if any. It is
// set from the resolved config by the caller. An empty string (or the
// default sentinel "container") means "not explicitly configured".
var (
	mu               sync.Mutex
	configuredBinary string
)

// SetConfiguredBinary records the container CLI path from the k3c config.
// Pass "" or the default literal "container" to mean "not explicitly set".
func SetConfiguredBinary(path string) {
	mu.Lock()
	defer mu.Unlock()
	if path == "container" {
		path = ""
	}
	configuredBinary = path
}

func configured() string {
	mu.Lock()
	defer mu.Unlock()
	return configuredBinary
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}

// Resolved describes the container CLI to invoke.
type Resolved struct {
	// Binary is the path (or bare name resolved via PATH) of the container
	// CLI to execute.
	Binary string
	// Env is extra environment in key=value form to set on every container
	// invocation (e.g. CONTAINER_INSTALL_ROOT for the bundled runtime). It
	// is empty when driving a host-installed container.
	Env []string
}

// The cached resolution is keyed on the configured binary it was computed
// with: commands may invoke the container CLI before the config is loaded
// (e.g. to find the default cluster), and a containerBinary configured
// after that must still take effect.
var (
	resolveMu   sync.Mutex
	resolvedFor *string
	resolved    Resolved
	resolveErr  error
)

// Resolve determines the container CLI and environment to use, performing
// bundled-runtime extraction at most once. The result is cached until the
// configured binary changes.
func Resolve() (Resolved, error) {
	c := configured()
	resolveMu.Lock()
	defer resolveMu.Unlock()
	if resolvedFor != nil && *resolvedFor == c {
		return resolved, resolveErr
	}
	resolved, resolveErr = resolve(c)
	resolvedFor = &c
	return resolved, resolveErr
}

func resolve(configured string) (Resolved, error) {
	// 1. explicit override path
	if p := os.Getenv("K3C_CONTAINER_BINARY"); p != "" {
		logger.Debug("container runtime: using K3C_CONTAINER_BINARY=" + p)
		return Resolved{Binary: p}, nil
	}

	// 2. force PATH lookup
	if truthy(os.Getenv("K3C_CONTAINER_FROM_PATH")) {
		logger.Debug("container runtime: using `container` from PATH (K3C_CONTAINER_FROM_PATH)")
		return Resolved{Binary: "container"}, nil
	}

	// 3. user-configured binary
	if configured != "" {
		logger.Debug("container runtime: using configured containerBinary=" + configured)
		return Resolved{Binary: configured}, nil
	}

	// 4. embedded bundled runtime
	if HasBundle() {
		dir, err := extractBundle()
		if err != nil {
			return Resolved{}, err
		}
		bin := filepath.Join(dir, "bin", "container")
		logger.Debug("container runtime: using bundled runtime at " + dir)
		return Resolved{
			Binary: bin,
			Env:    []string{"CONTAINER_INSTALL_ROOT=" + dir},
		}, nil
	}

	// 5. fallback
	logger.Debug("container runtime: no bundle embedded; falling back to `container` from PATH")
	return Resolved{Binary: "container"}, nil
}

// Binary returns just the resolved container CLI path. On resolution error
// it falls back to the bare name "container" so callers degrade gracefully.
func Binary() string {
	r, err := Resolve()
	if err != nil {
		logger.Debug("container runtime resolution failed: " + err.Error())
		return "container"
	}
	return r.Binary
}

// Env returns the extra environment to set on container invocations.
func Env() []string {
	r, err := Resolve()
	if err != nil {
		return nil
	}
	return r.Env
}

// GvnetBinary resolves the `gvnet` transparent-egress netstack helper. It is
// a separate binary (so the gvisor netstack does not bloat the k3c binary),
// shipped alongside the bundled runtime. Resolution precedence:
//
//  1. K3C_GVNET_BINARY — explicit path.
//  2. <bundle>/bin/gvnet — bundled next to the container runtime.
//  3. <dir of k3c executable>/gvnet — a sibling of the running k3c.
//  4. "gvnet" from PATH.
func GvnetBinary() string {
	if p := os.Getenv("K3C_GVNET_BINARY"); p != "" {
		return p
	}
	if HasBundle() {
		if dir, err := extractBundle(); err == nil {
			if bin := filepath.Join(dir, "bin", "gvnet"); fileExists(bin) {
				return bin
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		if bin := filepath.Join(filepath.Dir(exe), "gvnet"); fileExists(bin) {
			return bin
		}
	}
	return "gvnet"
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
