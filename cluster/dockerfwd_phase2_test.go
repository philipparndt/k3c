package cluster

import (
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"k3c/config"
	"k3c/dockerfwd"
)

// Phase 2: nested published ports reach the host through the in-guest forwarder
// over a unix socket (dialTarget → dialSidecarPort → dockerfwd), never the guest
// vmnet IP. These tests run the REAL forwarder and the REAL host dial paths
// against a fake "nested container", exercising the full mux protocol. (The
// --publish-socket host↔guest bridge itself is proven separately, OQ#1.)

// fakeNested stands in for a nested container's published port: it echoes
// "NESTED:" + the bytes it received, then closes. Returns its 127.0.0.1 port.
func fakeNested(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte("NESTED:" + string(buf)))
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := net.LookupPort("tcp", port)
	return p
}

// startForwarder runs the real in-guest forwarder on the host-side socket path
// that dialSidecarPort will dial (no --publish-socket bridge needed in-process).
func startForwarder(t *testing.T, cfg *config.Config) {
	t.Helper()
	sock := dockerForwardSocketPath(cfg)
	go func() { _ = dockerfwd.Serve(sock) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("forwarder socket %s never appeared", sock)
}

func shortBaseCfg(t *testing.T) *config.Config {
	t.Helper()
	// macOS caps unix-socket paths near 104 bytes; t.TempDir() overflows it.
	base, err := os.MkdirTemp("/tmp", "k3c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	return &config.Config{BaseDir: base}
}

func TestSidecarMuxForwardsThroughForwarder(t *testing.T) {
	cfg := shortBaseCfg(t)
	startForwarder(t, cfg)
	port := fakeNested(t)

	conn, err := dialSidecarPort(cfg, port)
	if err != nil {
		t.Fatalf("dialSidecarPort: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("PING")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	out, _ := io.ReadAll(conn)
	if string(out) != "NESTED:PING" {
		t.Fatalf("got %q, want %q (mux did not reach the nested port)", out, "NESTED:PING")
	}
}

func TestDialTargetDispatch(t *testing.T) {
	cfg := shortBaseCfg(t)
	startForwarder(t, cfg)
	port := fakeNested(t)

	// "sidecar:<port>" routes through the forwarder.
	c, err := dialTarget(cfg, sidecarTargetPrefix+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("dialTarget sidecar: %v", err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("PING"))
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	out, _ := io.ReadAll(c)
	if string(out) != "NESTED:PING" {
		t.Fatalf("sidecar dispatch got %q, want NESTED:PING", out)
	}

	// a plain host:port routes over tcp (straight to the fake nested server).
	c2, err := dialTarget(cfg, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dialTarget tcp: %v", err)
	}
	defer c2.Close()
	_, _ = c2.Write([]byte("PONG"))
	_ = c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	out2, _ := io.ReadAll(c2)
	if string(out2) != "NESTED:PONG" {
		t.Fatalf("tcp dispatch got %q, want NESTED:PONG", out2)
	}
}

func TestAcceptDockerForwardEndToEnd(t *testing.T) {
	cfg := shortBaseCfg(t)
	startForwarder(t, cfg)
	port := fakeNested(t)

	// the host-side per-port listener reconcileDockerPorts would create
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go acceptDockerForward(cfg, ln, port)

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial host listener: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("PING")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	out, _ := io.ReadAll(client)
	if string(out) != "NESTED:PING" {
		t.Fatalf("end-to-end got %q, want NESTED:PING (host listener did not reach the nested port via the forwarder)", out)
	}
}
