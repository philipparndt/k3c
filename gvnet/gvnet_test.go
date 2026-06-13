package gvnet

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/containers/gvisor-tap-vsock/pkg/transport"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
)

// The transparent-egress netstack must build a valid config, create its
// virtual network, and stand up the vfkit unixgram socket.
func TestGvnetInitializes(t *testing.T) {
	if _, err := virtualnetwork.New(config(Subnet, GatewayIP)); err != nil {
		t.Fatalf("virtualnetwork.New: %v", err)
	}
	// a custom per-VM subnet must also yield a valid config
	if _, err := virtualnetwork.New(config("192.168.130.0/24", "192.168.130.1")); err != nil {
		t.Fatalf("virtualnetwork.New (custom subnet): %v", err)
	}
	sock := filepath.Join(t.TempDir(), "gvnet.sock")
	conn, err := transport.ListenUnixgram("unixgram://" + sock)
	if err != nil {
		t.Fatalf("ListenUnixgram: %v", err)
	}
	defer conn.Close()
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("vfkit socket not created: %v", err)
	}
}
