// Package gvnet is the transparent-egress userspace network stack
// (gvisor-tap-vsock, the vfkit + gvproxy model) for a single container VM. The
// guest's NIC (a VZFileHandleNetworkDeviceAttachment) connects to the vfkit
// unixgram socket served here; the netstack terminates the guest's TCP/IP and
// RE-ORIGINATES every outbound connection via ordinary host sockets, so
// corporate egress works transparently (Zscaler carries the host-side
// connection — the same principle as the SNI gateway/CONNECT proxy) with no
// per-domain config, no pull-cache mirror, and no root/pf. DNS falls back to
// the host resolver.
//
// It lives in its own package (and ships as the standalone `gvnet` binary) so
// the gvisor netstack does not bloat the main k3c binary.
package gvnet

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/containers/gvisor-tap-vsock/pkg/transport"
	"github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
	"github.com/philipparndt/go-logger"
)

const (
	// host-facing virtual network the VM sees (distinct from the in-cluster
	// pod/service CIDRs, which live inside k3s)
	Subnet     = "192.168.127.0/24"
	GatewayIP  = "192.168.127.1"
	gatewayMAC = "5a:94:ef:e4:0c:dd"
	mtu        = 1500
)

// config builds the netstack configuration for the given subnet/gateway. The
// gateway NATs a virtual IP back to the host loopback so the VM can still
// reach k3c's host daemons (pull cache, registry forward, webhook) while
// everything else egresses transparently.
func config(subnet, gatewayIP string) *types.Configuration {
	return &types.Configuration{
		MTU:               mtu,
		Subnet:            subnet,
		GatewayIP:         gatewayIP,
		GatewayMacAddress: gatewayMAC,
		NAT: map[string]string{
			gatewayIP: "127.0.0.1",
		},
		DHCPStaticLeases: map[string]string{},
		Forwards:         map[string]string{},
	}
}

// Run serves the netstack on the default subnet (192.168.127.0/24).
func Run(ctx context.Context, socketURI string) error {
	return RunNet(ctx, socketURI, Subnet, GatewayIP)
}

// RunNet serves the netstack for the VM connected on the vfkit unixgram socket
// (e.g. "unixgram:///path/to/gvnet.sock") using the given subnet (CIDR) and
// gateway IP, until ctx is done or the connection drops. Each VM gets its own
// netstack on a distinct subnet (the runtime rejects overlapping networks).
func RunNet(ctx context.Context, socketURI, subnet, gatewayIP string) error {
	vn, err := virtualnetwork.New(config(subnet, gatewayIP))
	if err != nil {
		return fmt.Errorf("gvnet: new virtual network: %w", err)
	}
	if u, err := url.Parse(socketURI); err == nil && u.Path != "" {
		_ = os.Remove(u.Path) // stale socket from a previous run
	}
	conn, err := transport.ListenUnixgram(socketURI)
	if err != nil {
		return fmt.Errorf("gvnet: listen %s: %w", socketURI, err)
	}
	logger.Info("gvnet: transparent-egress netstack listening on " + socketURI)
	defer conn.Close()
	vfkitConn, err := transport.AcceptVfkit(conn)
	if err != nil {
		return fmt.Errorf("gvnet: accept vfkit: %w", err)
	}
	return vn.AcceptVfkit(ctx, vfkitConn)
}
