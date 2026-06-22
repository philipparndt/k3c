// k3c runs local k3s clusters on Apple `container`
// (https://github.com/apple/container) — like k3d, but for Apple's native
// container runtime instead of Docker.
package main

import (
	"os"

	"github.com/philipparndt/go-logger"

	"k3c/cmd"
)

func main() {
	// Default to info so routine debug lines (e.g. container-runtime
	// resolution) stay out of the way; opt into debug with K3C_LOG_LEVEL.
	level := os.Getenv("K3C_LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	logger.Init(level, logger.CLICompact())
	// keep stdout clean for command output (tables, JSON, kubeconfigs);
	// logs go to stderr like any well-behaved CLI
	logger.LogTo(os.Stderr)
	cmd.Execute()
}
