// Command gvnet runs the transparent-egress userspace network stack
// (gvisor-tap-vsock) for a single container VM. The container runtime's gvnet
// network spawns one instance per VM, passing the path of the vfkit unixgram
// socket the VM's file-handle NIC connects to. The netstack terminates the
// guest's TCP/IP and re-originates every connection from host sockets, so
// corporate egress (Zscaler) works transparently with no per-domain config.
//
// It is a separate binary so the gvisor netstack does not bloat the main k3c
// binary, and so the runtime can spawn it as an ordinary helper process.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"k3c/gvnet"
)

func main() {
	socket := flag.String("socket", "", "vfkit unixgram socket URI the VM NIC connects to (e.g. unixgram:///path/gvnet.sock)")
	subnet := flag.String("subnet", gvnet.Subnet, "CIDR of the virtual network the VM sees")
	gateway := flag.String("gateway", gvnet.GatewayIP, "gateway IP within the subnet")
	flag.Parse()
	if *socket == "" {
		fmt.Fprintln(os.Stderr, "gvnet: -socket is required")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := gvnet.RunNet(ctx, *socket, *subnet, *gateway); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "gvnet:", err)
		os.Exit(1)
	}
}
