package cluster

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

// Transparent egress via a gvisor-tap-vsock userspace netstack (the OrbStack /
// vfkit + gvproxy model). The container VM's NIC is backed by a
// VZFileHandleNetworkDeviceAttachment connected to the unixgram socket served
// here; the netstack terminates the VM's TCP/IP and RE-ORIGINATES every
// outbound connection via ordinary host sockets — so corporate egress works
// transparently (Zscaler carries the host-side connection, exactly like the
// SNI gateway / CONNECT proxy already do) with no per-domain config, no pull
// cache mirror, and no root/pf. DNS is answered by the embedded resolver,
// which falls back to the host's system resolver for everything else.

const (
	// host-facing virtual network the VM sees (distinct from the in-cluster
	// pod/service CIDRs, which live inside k3s)
	gvnetSubnet     = "192.168.127.0/24"
	gvnetGatewayIP  = "192.168.127.1"
	gvnetGatewayMAC = "5a:94:ef:e4:0c:dd"
	gvnetMTU        = 1500
)

// gvnetConfig builds the netstack configuration. The gateway NATs a virtual
// IP back to the host loopback so the VM can still reach k3c's host daemons
// (pull cache, registry forward, webhook) while everything else egresses
// transparently.
func gvnetConfig() *types.Configuration {
	return &types.Configuration{
		MTU:               gvnetMTU,
		Subnet:            gvnetSubnet,
		GatewayIP:         gvnetGatewayIP,
		GatewayMacAddress: gvnetGatewayMAC,
		// reach the host itself (k3c daemons) from the VM via the gateway IP
		NAT: map[string]string{
			gvnetGatewayIP: "127.0.0.1",
		},
		DHCPStaticLeases: map[string]string{},
		Forwards:         map[string]string{},
		DNSSearchDomains: nil,
	}
}

// serveGvnet runs the netstack, serving the VM connected on the vfkit unixgram
// socket (e.g. "unixgram:///path/to/gvnet.sock"). It blocks until ctx is done
// or the connection drops.
func serveGvnet(ctx context.Context, socketURI string) error {
	vn, err := virtualnetwork.New(gvnetConfig())
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
