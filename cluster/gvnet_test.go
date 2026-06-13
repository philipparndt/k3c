package cluster

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/containers/gvisor-tap-vsock/pkg/transport"
	"github.com/containers/gvisor-tap-vsock/pkg/virtualnetwork"
)

// Milestone 1: the transparent-egress netstack must build a valid config,
// create its virtual network, and stand up the vfkit unixgram socket.
func TestGvnetInitializes(t *testing.T) {
	if _, err := virtualnetwork.New(gvnetConfig()); err != nil {
		t.Fatalf("virtualnetwork.New: %v", err)
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
