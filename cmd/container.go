package cmd

import (
	"errors"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"k3c/cluster"
	"k3c/config"
	"k3c/runtime"
)

// containerCmd passes its arguments straight to the resolved container CLI
// (the bundled runtime by default), so the embedded tool is usable without
// installing Apple container separately: k3c container ls -a
var containerCmd = &cobra.Command{
	Use:   "container [ARGS...]",
	Short: "Run the embedded Apple container CLI (k3c container ls -a, ...)",
	Long: `Run the resolved Apple container CLI with k3c's runtime environment.

The arguments are passed through verbatim. This is the bundled runtime by
default, or the binary set via containerBinary in the k3c config — the same
one every other k3c command uses.`,
	// every flag belongs to the container CLI, including --help
	DisableFlagParsing: true,
	Run: func(cmd *cobra.Command, args []string) {
		// honour a configured containerBinary before resolving the runtime
		if cfg, err := config.Resolve("", configFile); err == nil {
			cluster.SetContainerBinary(cfg.ContainerBinary)
		}
		if _, err := runtime.Resolve(); err != nil {
			fail(err)
		}
		c := runtime.Command(args...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				os.Exit(exitErr.ExitCode())
			}
			fail(err)
		}
	},
}

// containerSystemRestartCmd restarts the embedded container system so a
// freshly extracted bundled runtime is applied (new install root, newly
// registered plugins) and records the version, marking the update done.
// Hidden: interactive shells get this via EnsureSystem's prompt; the TUI —
// whose alt-screen cannot host that prompt — runs this from its own restart
// dialog instead.
var containerSystemRestartCmd = &cobra.Command{
	Use:    "container-system-restart",
	Hidden: true,
	Short:  "Restart the embedded container system to apply an updated runtime",
	Args:   cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if cfg, err := config.Resolve("", configFile); err == nil {
			cluster.SetContainerBinary(cfg.ContainerBinary)
		}
		if _, err := runtime.Resolve(); err != nil {
			fail(err)
		}
		fail(runtime.RestartSystem())
	},
}

func init() {
	rootCmd.AddCommand(containerCmd, containerSystemRestartCmd)
}
