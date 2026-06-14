package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/philipparndt/go-logger"
)

// initImageRef is the guest init image the runtime needs loaded before it
// can create VMs. The bundled payload ships it as init.tar at the install
// root; EnsureSystem loads it if it is not already present.
const (
	initImageRef = "vminit:latest"
	initTarName  = "init.tar"
)

// Command builds an *exec.Cmd for the resolved container CLI with the
// resolved extra environment applied (e.g. CONTAINER_INSTALL_ROOT). Callers
// use this instead of exec.Command so bundled-runtime invocations get the
// right binary and environment.
func Command(args ...string) *exec.Cmd {
	cmd := exec.Command(Binary(), args...)
	if env := Env(); len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd
}

// Output runs the resolved container CLI and returns its trimmed combined
// output.
func Output(args ...string) (string, error) {
	out, err := Command(args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

var (
	ensureOnce sync.Once
	ensureErr  error
)

// EnsureSystem makes the resolved runtime ready to create clusters: it
// resolves (extracting the bundle if needed), ensures the system services
// are started, and loads the init image when it is missing. It runs its
// work at most once per process.
func EnsureSystem() error {
	ensureOnce.Do(func() {
		ensureErr = ensureSystem()
	})
	return ensureErr
}

func ensureSystem() error {
	if _, err := Resolve(); err != nil {
		return err
	}

	// Start the launchd-managed system services if not already running.
	// Without a configured default kernel `system start` PROMPTS to install
	// one; k3c often runs it from scripts, so always answer yes via
	// --enable-kernel-install (the interactive default).
	if _, err := Output("system", "status"); err != nil {
		logger.Info("starting container system")
		if out, err := Output("system", "start", "--enable-kernel-install"); err != nil {
			return fmt.Errorf("could not start container system: %s", out)
		}
	}

	// A `system start` that died half-way (e.g. on the kernel install
	// prompt of older k3c builds) can leave the apiserver responding while
	// its plugins were never registered: `system status` looks fine but
	// every image operation fails with a plugin-not-found error. Detect
	// that state and restart the system services.
	if out, err := Output("image", "ls"); err != nil && pluginMissing(out) {
		logger.Info("container system is missing its plugins (an aborted first start); restarting it")
		_, _ = Output("system", "stop")
		if out, err := Output("system", "start", "--enable-kernel-install"); err != nil {
			return fmt.Errorf("could not restart container system: %s", out)
		}
	}

	if err := ensureInitImage(); err != nil {
		return err
	}
	return nil
}

// ensureInitImage loads the bundled init image (vminit:latest) if it is not
// already present. This only applies to the bundled runtime, where the
// payload ships init.tar at the install root. A host-installed container is
// expected to already have its init image (installed by its own installer).
func ensureInitImage() error {
	root := installRoot()
	if root == "" {
		return nil // host-installed runtime; not our concern
	}
	if initImagePresent() {
		return nil
	}
	tar := filepath.Join(root, initTarName)
	if _, err := os.Stat(tar); err != nil {
		// TODO(release): the init image is only loadable if the bundle was
		// assembled with init.tar. Without it, `container system start`
		// must obtain the init image another way. See runtime/payload/README.md.
		logger.Debug("bundled init image " + tar + " not found; skipping load")
		return nil
	}
	logger.Info("loading bundled init image (" + initImageRef + ")")
	if out, err := Output("image", "load", "-i", tar); err != nil {
		return fmt.Errorf("loading init image: %s", out)
	}
	return nil
}

// pluginMissing reports whether output is the container CLI's
// plugin-not-found error (either the CLI dispatch or the service variant).
func pluginMissing(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "plugin") && strings.Contains(lower, "not found")
}

// installRoot returns the CONTAINER_INSTALL_ROOT from the resolved env, or
// "" when none is set (host-installed runtime).
func installRoot() string {
	for _, kv := range Env() {
		if strings.HasPrefix(kv, "CONTAINER_INSTALL_ROOT=") {
			return strings.TrimPrefix(kv, "CONTAINER_INSTALL_ROOT=")
		}
	}
	return ""
}

func initImagePresent() bool {
	out, err := Output("image", "ls")
	if err != nil {
		return false
	}
	// The runtime resolves the init image as the bare ref "vminit:latest", so
	// match that EXACTLY (name "vminit", tag "latest"). A loose "vminit"
	// substring also matches the unrelated ghcr.io/apple/containerization/
	// vminit:<version> images, which would wrongly skip loading the bundled
	// init image and leave vminit:latest unresolvable at container-run time.
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "vminit" && f[1] == "latest" {
			return true
		}
	}
	return false
}
