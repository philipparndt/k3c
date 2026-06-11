package cmd

import (
	"os"

	"github.com/philipparndt/go-logger"
	"github.com/spf13/cobra"

	"k3c/cluster"
	"k3c/config"
)

var configFile string

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "k3c",
	Short: "Run local k3s clusters on Apple container",
	Long: `k3c — like k3d, but for Apple's native container runtime
(https://github.com/apple/container) instead of Docker.

k3c works around the Apple container VM kernel limitations (no nftables,
vxlan, or br_netfilter) and provides image pulls and pod HTTPS egress
through host-side daemons when a corporate full-tunnel VPN blocks the VMs'
outbound connectivity.

Configuration is layered: built-in defaults, then ~/.config/k3c/config.yaml
(user defaults, e.g. corporate CA and registry mirrors), then ./k3c.yaml
(project config; override with --config or K3C_CONFIG).`,
}

// Execute adds all child commands to the root command and sets flags
// appropriately. This is called by main.main().
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "",
		"project config file (default ./k3c.yaml, env K3C_CONFIG)")
}

// loadConfig resolves the layered configuration for the cluster named in
// args (if any).
func loadConfig(args []string) *config.Config {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	cfg, err := config.Resolve(name, configFile)
	if err != nil {
		logger.Panic("Failed to load config", err)
	}
	cluster.SetContainerBinary(cfg.ContainerBinary)
	return cfg
}

// loadConfigDefault resolves the cluster config like loadConfig, but when
// no name is given the active cluster (or the only existing one) is used.
func loadConfigDefault(args []string) *config.Config {
	if len(args) == 0 {
		// Finding the default cluster invokes the container CLI before the
		// cluster config is loaded: configure the binary from the global
		// config first so the right runtime is used.
		if cfg, err := config.Resolve("", configFile); err == nil {
			cluster.SetContainerBinary(cfg.ContainerBinary)
		}
		if name := cluster.ActiveClusterName(); name != "" {
			args = []string{name}
		} else if name := cluster.OnlyClusterName(); name != "" {
			args = []string{name}
		}
	}
	return loadConfig(args)
}

func fail(err error) {
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}
