package cmd

import (
	"github.com/spf13/cobra"

	"k3c/web"
)

var (
	webPort   int
	webAddr   string
	webNoOpen bool
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Live system diagram in the browser (alternative to k3c ui)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fail(web.Serve(loadConfigDefault(nil), web.Options{
			Addr: webAddr,
			Port: webPort,
			Open: !webNoOpen,
		}))
	},
}

func init() {
	webCmd.Flags().IntVar(&webPort, "port", 7654, "listen port (a free port is picked if busy)")
	webCmd.Flags().StringVar(&webAddr, "addr", "127.0.0.1", "bind address")
	webCmd.Flags().BoolVar(&webNoOpen, "no-open", false, "do not open a browser")
	rootCmd.AddCommand(webCmd)
}
