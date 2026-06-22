package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"k3c/config"
	"k3c/runtime"
	"k3c/ui"
	"k3c/version"
)

// envInfo is the machine-readable form behind `k3c info --json`.
type envInfo struct {
	Version          version.Info `json:"version"`
	BundledContainer string       `json:"bundledContainer,omitempty"`
	Runtime          runtimeInfo  `json:"runtime"`
	Config           configInfo   `json:"config"`
}

type runtimeInfo struct {
	Binary string `json:"binary"`
	Source string `json:"source"`
	CLI    string `json:"cli"`
	Error  string `json:"error,omitempty"`
}

type configInfo struct {
	StateDir        string `json:"stateDir"`
	UserConfig      string `json:"userConfig"`
	UserConfigFound bool   `json:"userConfigFound"`
	ProjectConfig   string `json:"projectConfig"`
	ContainerBinary string `json:"containerBinary"`
	Cluster         string `json:"cluster"`
	Context         string `json:"context"`
}

// infoCmd prints an at-a-glance summary of the k3c environment: version, the
// resolved container runtime (which binary, why, and its CLI version), and
// where configuration is read from. It is read-only and does not start the
// container system, so it stays useful for diagnostics even when the daemon
// is down.
var infoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show version, container runtime, and config in use",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadConfig(nil)
		userCfg, userFound := userConfig()

		rt := runtimeInfo{}
		if r, err := runtime.Resolve(); err == nil {
			rt.Binary, rt.Source = r.Binary, r.Source
			if cli, err := runtime.Output("--version"); err == nil {
				rt.CLI = firstLine(cli)
			} else {
				rt.CLI = firstLine(cli)
				rt.Error = "container CLI not responding"
			}
		} else {
			rt.Error = err.Error()
		}

		data := envInfo{
			Version:          version.Get(),
			BundledContainer: runtime.BundledContainerVersion(),
			Runtime:          rt,
			Config: configInfo{
				StateDir:        cfg.BaseDir,
				UserConfig:      userCfg,
				UserConfigFound: userFound,
				ProjectConfig:   cfg.ConfigFile,
				ContainerBinary: cfg.ContainerBinary,
				Cluster:         cfg.Cluster,
				Context:         cfg.KubeContext,
			},
		}

		if ui.JSON() {
			fail(ui.EmitJSON(data))
			return
		}

		ui.Section("k3c")
		ui.KV("version", data.Version.Version, 9)
		ui.KV("commit", data.Version.GitCommit, 9)
		ui.KV("platform", data.Version.Platform, 9)

		ui.Section("container runtime")
		if rt.Error != "" && rt.Binary == "" {
			ui.KV("error", ui.Err(rt.Error), 9)
		} else {
			ui.KV("binary", rt.Binary, 9)
			ui.KV("source", rt.Source, 9)
			if rt.Error != "" {
				ui.KV("cli", ui.Err(rt.Error)+" ("+rt.CLI+")", 9)
			} else {
				ui.KV("cli", rt.CLI, 9)
			}
		}
		if data.BundledContainer != "" {
			ui.KV("bundled", data.BundledContainer, 9)
		}

		ui.Section("config")
		ui.KV("state", cfg.BaseDir, 9)
		ui.KV("user", userConfigLabel(userCfg, userFound), 9)
		ui.KV("project", orNone(cfg.ConfigFile), 9)
		ui.KV("binary", orNone(cfg.ContainerBinary), 9)
		ui.KV("cluster", cfg.Cluster+" "+ui.Muted("(context "+cfg.KubeContext+")"), 9)
	},
}

// userConfig returns the user config path and whether it exists on disk.
func userConfig() (string, bool) {
	dir := config.UserConfigDir()
	if dir == "" {
		return "", false
	}
	p := filepath.Join(dir, "config.yaml")
	_, err := os.Stat(p)
	return p, err == nil
}

func userConfigLabel(path string, found bool) string {
	if path == "" {
		return ui.Muted("none")
	}
	if !found {
		return path + " " + ui.Muted("(not present)")
	}
	return path
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return ui.Muted("none")
	}
	return s
}

func init() {
	rootCmd.AddCommand(infoCmd)
}
