// k3c runs local k3s clusters on Apple `container`
// (https://github.com/apple/container) — like k3d, but for Apple's native
// container runtime instead of Docker.
package main

import (
	"k3c/cmd"

	"github.com/philipparndt/go-logger"
)

func main() {
	logger.Init("debug", logger.CLICompact())
	cmd.Execute()
}
