package cluster

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"k3c/config"
)

// The sidecar engine must be reached at the stable Apple-published loopback
// endpoint, never the guest vmnet IP (which is not host-reachable, see the
// docker-sidecar-host-forwarder change). These tests pin that contract.

func TestDockerEngineEndpointIsLoopback(t *testing.T) {
	for _, port := range []string{"2375", "2400"} {
		got := dockerEngineEndpoint(&config.Config{DockerPort: port})
		want := "127.0.0.1:" + port
		if got != want {
			t.Fatalf("dockerEngineEndpoint(DockerPort=%q) = %q, want %q", port, got, want)
		}
		if strings.Contains(got, "192.168.") {
			t.Fatalf("engine endpoint must not target a vmnet IP, got %q", got)
		}
	}
}

func TestDockerPublishedPortsQueriesEndpointAndParses(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`[
			{"Ports":[{"IP":"","PublicPort":8080,"Type":"tcp"}]},
			{"Ports":[{"IP":"127.0.0.1","PublicPort":9090,"Type":"tcp"}]},
			{"Ports":[
				{"IP":"0.0.0.0","PublicPort":7000,"Type":"tcp"},
				{"IP":"::","PublicPort":7000,"Type":"tcp"},
				{"IP":"0.0.0.0","PublicPort":7000,"Type":"tcp"},
				{"IP":"0.0.0.0","PublicPort":53,"Type":"udp"},
				{"IP":"0.0.0.0","PublicPort":0,"Type":"tcp"}
			]}
		]`))
	}))
	defer ts.Close()

	endpoint := strings.TrimPrefix(ts.URL, "http://")
	binds := dockerPublishedPorts(endpoint)

	if gotPath != "/containers/json" {
		t.Fatalf("queried path = %q, want /containers/json", gotPath)
	}

	got := map[string]bool{}
	for _, b := range binds {
		got[b.addr()] = true
	}
	want := []string{"0.0.0.0:8080", "127.0.0.1:9090", "0.0.0.0:7000"}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing expected bind %q (got %v)", w, got)
		}
	}
	// udp, the IPv6 twin, PublicPort 0, and the duplicate must all be dropped.
	if len(binds) != len(want) {
		t.Errorf("got %d binds %v, want exactly %d %v", len(binds), got, len(want), want)
	}
}

// TestStartDockerSocketForwardsToLoopbackEngine exercises the real
// startDockerSocket: the host unix socket must forward to the loopback engine
// endpoint (127.0.0.1:<DockerPort>), the Apple-published port — proving the
// engine is reachable without dialing the guest vmnet IP.
func TestStartDockerSocketForwardsToLoopbackEngine(t *testing.T) {
	// a fake engine on loopback, standing in for the Apple-published
	// 127.0.0.1:<DockerPort> forward of the sidecar's dockerd.
	engine, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	_, port, _ := net.SplitHostPort(engine.Addr().String())
	go func() {
		c, err := engine.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		_, _ = c.Write([]byte("PONG:" + string(buf)))
	}()

	// a short base dir: macOS unix-socket paths are capped near 104 bytes, and
	// the default t.TempDir() path overflows it.
	base, err := os.MkdirTemp("/tmp", "k3c")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	cfg := &config.Config{BaseDir: base, DockerPort: port}
	startDockerSocket(cfg)

	path := dockerSocketPath(cfg)
	var conn net.Conn
	for i := 0; i < 100; i++ {
		if conn, err = net.Dial("unix", path); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial host docker socket %s: %v", path, err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("PING")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	out, _ := io.ReadAll(conn)
	if string(out) != "PONG:PING" {
		t.Fatalf("forwarded response = %q, want %q (socket did not reach the loopback engine)", out, "PONG:PING")
	}
}
