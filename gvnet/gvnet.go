// Package gvnet is the transparent-egress userspace network stack
// (gvisor-tap-vsock, the vfkit + gvproxy model) for a single container VM. The
// guest's NIC (a VZFileHandleNetworkDeviceAttachment) connects to the vfkit
// unixgram socket served here; the netstack terminates the guest's TCP/IP and
// RE-ORIGINATES every outbound connection via ordinary host sockets, so
// corporate egress works transparently (the corporate VPN/proxy carries the
// host-side connection — the same principle as the SNI gateway/CONNECT proxy) with no
// per-domain config, no pull-cache mirror, and no root/pf. DNS falls back to
// the host resolver.
//
// It lives in its own package (and ships as the standalone `gvnet` binary) so
// the gvisor netstack does not bloat the main k3c binary.
package gvnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync/atomic"
	"syscall"

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

// dynamicUnixgramConn is a net.Conn over the netstack's listening unixgram
// socket that always replies to the source of the most-recently-received
// datagram, instead of a single peer address latched once at accept time.
//
// The guest NIC connects to the netstack from a per-VM datagram socket
// (.../k3c-gv-<pid>-<rand>.sock, bound by the runtime's GvnetInterfaceStrategy).
// gvisor-tap-vsock's stock connectedUnixgramConn latches that address once and
// writes every reply there for the life of the connection. When the VM restarts
// (a new entrypoint, `docker down`/`up`, or a recreate) it reconnects from a NEW
// socket path while this netstack keeps running, so replies would go to the dead
// old address and egress silently breaks — or a write to the removed socket
// returns ECONNREFUSED and the switch tears the whole netstack down. Tracking
// the live peer per received datagram (and dropping frames when the peer is
// momentarily gone, rather than failing) keeps one long-lived netstack usable
// across the VM's restarts — which matters because the guest NIC is connect()ed
// to this socket's inode, so the netstack must outlive the VM and never restart.
type dynamicUnixgramConn struct {
	*net.UnixConn
	peer atomic.Pointer[net.UnixAddr]
}

func newDynamicUnixgramConn(conn *net.UnixConn, initial *net.UnixAddr) *dynamicUnixgramConn {
	c := &dynamicUnixgramConn{UnixConn: conn}
	if initial != nil && initial.Name != "" {
		c.peer.Store(initial)
	}
	return c
}

func (c *dynamicUnixgramConn) Read(b []byte) (int, error) {
	n, addr, err := c.UnixConn.ReadFromUnix(b)
	if addr != nil && addr.Name != "" {
		c.peer.Store(addr) // remember the live guest socket as the return path
	}
	return n, err
}

func (c *dynamicUnixgramConn) Write(b []byte) (int, error) {
	peer := c.peer.Load()
	if peer == nil {
		return len(b), nil // no datagram seen yet: no return path, drop
	}
	n, err := c.UnixConn.WriteToUnix(b, peer)
	if err != nil && (errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)) {
		// the guest socket went away (VM stop/restart): drop this frame rather
		// than let the switch tear the netstack down — the next received
		// datagram re-learns the new return path and egress resumes
		return len(b), nil
	}
	return n, err
}

func (c *dynamicUnixgramConn) RemoteAddr() net.Addr {
	if peer := c.peer.Load(); peer != nil {
		return peer
	}
	return &net.UnixAddr{Net: "unixgram"}
}

// RunNet serves the netstack for the VM connected on the vfkit unixgram socket
// (e.g. "unixgram:///path/to/gvnet.sock") using the given subnet (CIDR) and
// gateway IP, until ctx is done. Each VM gets its own netstack on a distinct
// subnet (the runtime rejects overlapping networks). The netstack survives the
// VM restarting on a new NIC socket (see dynamicUnixgramConn), so it can be
// started once before the VM and live as long as the VM does.
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
	// Unblock a pending Read/Accept on cancellation so the netstack exits.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		// Block for the guest's first datagram and consume an optional VFKT
		// handshake; the peeked address only seeds the initial return path,
		// which dynamicUnixgramConn then keeps current per received datagram.
		vfkitConn, err := transport.AcceptVfkit(conn)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("gvnet: accept vfkit: %w", err)
		}
		initial, _ := vfkitConn.RemoteAddr().(*net.UnixAddr)
		if err := vn.AcceptVfkit(ctx, newDynamicUnixgramConn(conn, initial)); err != nil && ctx.Err() == nil {
			// A fatal read error ended the connection (writes no longer kill it);
			// loop to re-accept so the netstack keeps serving the VM.
			logger.Error("gvnet: netstack connection ended, re-accepting: " + err.Error())
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}
