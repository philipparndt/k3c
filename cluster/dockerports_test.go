package cluster

import (
	"bufio"
	"fmt"
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

// TestClassifyEngineHead pins the request classifier that decides the transport:
// hijacked/upgrade streams go over the full-duplex --publish-socket bridge (which
// carries them), everything else over the HOL-free loopback endpoint.
func TestClassifyEngineHead(t *testing.T) {
	cases := []struct {
		name           string
		head           string
		parsed, hijack bool
	}{
		{"inspect", "GET /v1.43/containers/abc/json HTTP/1.1\r\nHost: d", true, false},
		{"logs no-follow", "GET /v1.43/containers/abc/logs?follow=0&stdout=1 HTTP/1.1\r\nHost: d", true, false},
		{"logs follow", "GET /v1.43/containers/abc/logs?follow=1 HTTP/1.1\r\nHost: d", true, false},
		{"archive PUT (docker cp)", "PUT /v1.43/containers/abc/archive?path=%2Ftmp HTTP/1.1\r\nHost: d\r\nContent-Length: 10", true, false},
		{"exec start upgrade", "POST /v1.43/exec/abc/start HTTP/1.1\r\nUpgrade: tcp\r\nConnection: Upgrade", true, true},
		{"attach path", "POST /v1.43/containers/abc/attach?stream=1&stdout=1 HTTP/1.1\r\nHost: d", true, true},
		{"attach ws path", "GET /v1.43/containers/abc/attach/ws HTTP/1.1\r\nHost: d", true, true},
		{"connection upgrade header", "POST /v1.43/anything HTTP/1.1\r\nConnection: Upgrade", true, true},
		{"connection upgrade mixed case", "POST /v1.43/x HTTP/1.1\r\nConnection: keep-alive, Upgrade", true, true},
		{"not http", "PING some garbage bytes", false, false},
		{"empty", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed, hijack := classifyEngineHead([]byte(c.head))
			if parsed != c.parsed || hijack != c.hijack {
				t.Fatalf("classifyEngineHead(%q) = (parsed=%v, hijack=%v), want (%v, %v)",
					c.head, parsed, hijack, c.parsed, c.hijack)
			}
		})
	}
}

// startLoopbackEngine starts a fake engine on 127.0.0.1 standing in for the
// Apple-published loopback publish. It serves each connection in its own
// goroutine (so, like the real loopback publish, it never head-of-line-blocks):
// a /logs request gets a large, deliberately un-terminated body; anything else
// gets a short marked response. Returns the port to set as cfg.DockerPort.
func startLoopbackEngine(t *testing.T, marker string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeEngine(c, marker)
		}
	}()
	return port
}

// serveFakeEngine reads one HTTP request head and replies. For /logs it writes a
// big body that a non-draining client will leave back-pressured (so the E1
// regression exercises a genuinely undrained stream); for everything else a
// short body containing marker so the caller can tell which upstream served it.
func serveFakeEngine(c net.Conn, marker string) {
	defer c.Close()
	br := bufio.NewReader(c)
	head, err := readHead(br)
	if err != nil {
		return
	}
	if strings.Contains(head, "/logs") {
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 1000000\r\n\r\n")
		_, _ = c.Write(make([]byte, 1000000)) // blocks until drained; fine, own goroutine
		return
	}
	_, _ = fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(marker), marker)
}

// readHead consumes bytes from br up to and including the CRLFCRLF that ends an
// HTTP head, returning the head (without the terminator).
func readHead(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return sb.String(), err
		}
		if line == "\r\n" || line == "\n" {
			return sb.String(), nil
		}
		sb.WriteString(line)
	}
}

// dialHostSocket dials the host engine socket, retrying while startDockerSocket
// binds it.
func dialHostSocket(t *testing.T, cfg *config.Config) net.Conn {
	t.Helper()
	path := dockerSocketPath(cfg)
	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		if conn, err = net.Dial("unix", path); err == nil {
			return conn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("dial host docker socket %s: %v", path, err)
	return nil
}

func tempBase(t *testing.T) *config.Config {
	t.Helper()
	// macOS caps unix-socket paths near 104 bytes; the default t.TempDir() path
	// overflows it, so use a short /tmp dir.
	base, err := os.MkdirTemp("/tmp", "k3c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	return &config.Config{BaseDir: base}
}

// TestEngineNonHijackRoutesToLoopback pins remediation B's common path: a plain
// request/response (here an Inspect) is spliced to the HOL-free loopback
// endpoint, not the bridge — even when a bridge is present.
func TestEngineNonHijackRoutesToLoopback(t *testing.T) {
	cfg := tempBase(t)
	cfg.DockerPort = startLoopbackEngine(t, "LOOPBACK")

	// A bridge that, if ever used, would record it — and would fail the test.
	bridgeUsed := recordBridge(t, cfg)

	startDockerSocket(cfg)
	conn := dialHostSocket(t, cfg)
	defer conn.Close()

	_, _ = io.WriteString(conn, "GET /v1.43/containers/x/json HTTP/1.1\r\nHost: d\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, _ := io.ReadAll(conn)
	if !strings.Contains(string(resp), "LOOPBACK") {
		t.Fatalf("inspect response = %q, want it served by the loopback engine", resp)
	}
	if *bridgeUsed {
		t.Fatal("non-hijack request was routed to the --publish-socket bridge; must use loopback")
	}
}

// TestEngineHijackRoutesToBridge pins that a hijacked/upgrade stream is spliced
// over the full-duplex --publish-socket bridge (Apple's loopback publish drops
// hijacks). The fake bridge reads the dockerfwd port header, proving the guest
// engine port was selected.
func TestEngineHijackRoutesToBridge(t *testing.T) {
	cfg := tempBase(t)
	// loopback points at a closed port so a mis-route to loopback would fail.
	cfg.DockerPort = "1"

	fwd, err := net.Listen("unix", dockerForwardSocketPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	go func() {
		c, err := fwd.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		hdr, err := br.ReadString('\n') // dockerfwd port header
		if err != nil {
			return
		}
		if _, err := readHead(br); err != nil { // the hijack request head
			return
		}
		body := "BRIDGE:" + strings.TrimSpace(hdr)
		_, _ = fmt.Fprintf(c, "HTTP/1.1 101 Switching Protocols\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	}()

	startDockerSocket(cfg)
	conn := dialHostSocket(t, cfg)
	defer conn.Close()

	_, _ = io.WriteString(conn, "POST /v1.43/exec/abc/start HTTP/1.1\r\nUpgrade: tcp\r\nConnection: Upgrade\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, _ := io.ReadAll(conn)
	if !strings.Contains(string(resp), "BRIDGE:2375") {
		t.Fatalf("hijack response = %q, want it served over the bridge with guest engine port 2375", resp)
	}
}

// TestEngineHijackFallsBackToLoopbackWhenBridgeAbsent documents the degraded
// path: a sidecar created before --publish-socket has no bridge socket, so a
// hijack request falls back to the loopback endpoint (where Apple drops the
// hijack) — no worse than before, and such a sidecar should be recreated.
func TestEngineHijackFallsBackToLoopbackWhenBridgeAbsent(t *testing.T) {
	cfg := tempBase(t)
	cfg.DockerPort = startLoopbackEngine(t, "LOOPBACK")
	// no bridge socket exists at dockerForwardSocketPath(cfg)

	startDockerSocket(cfg)
	conn := dialHostSocket(t, cfg)
	defer conn.Close()

	_, _ = io.WriteString(conn, "POST /v1.43/exec/abc/start HTTP/1.1\r\nUpgrade: tcp\r\nConnection: Upgrade\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, _ := io.ReadAll(conn)
	if !strings.Contains(string(resp), "LOOPBACK") {
		t.Fatalf("hijack with no bridge = %q, want fallback to the loopback endpoint", resp)
	}
}

// TestEngineBackpressuredLogsDoNotBlockInspect is the E1 regression: with an
// open-but-undrained log stream held on one connection, a concurrent Inspect on
// a second connection must complete promptly. Both are non-hijack, so both route
// to the HOL-free loopback endpoint. A HOL-trap bridge is present so that, if the
// code ever regressed to route these over the --publish-socket bridge, the
// Inspect would hang and this test would time out.
func TestEngineBackpressuredLogsDoNotBlockInspect(t *testing.T) {
	cfg := tempBase(t)
	cfg.DockerPort = startLoopbackEngine(t, "LOOPBACK")
	startHOLTrapBridge(t, cfg)

	startDockerSocket(cfg)

	// conn1: open a follow=false logs stream and read ONLY the response head,
	// leaving the body undrained (back-pressured).
	logs := dialHostSocket(t, cfg)
	defer logs.Close()
	_, _ = io.WriteString(logs, "GET /v1.43/containers/x/logs?follow=0&stdout=1 HTTP/1.1\r\nHost: d\r\n\r\n")
	if _, err := readHead(bufio.NewReader(logs)); err != nil {
		t.Fatalf("reading logs response head: %v", err)
	}
	// deliberately do NOT drain the body

	// conn2: a concurrent Inspect must not stall behind the undrained stream.
	insp := dialHostSocket(t, cfg)
	defer insp.Close()
	_, _ = io.WriteString(insp, "GET /v1.43/containers/x/json HTTP/1.1\r\nHost: d\r\n\r\n")

	done := make(chan string, 1)
	go func() {
		resp, _ := io.ReadAll(insp)
		done <- string(resp)
	}()
	select {
	case resp := <-done:
		if !strings.Contains(resp, "LOOPBACK") {
			t.Fatalf("inspect response = %q, want it served by the loopback engine", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inspect stalled behind a back-pressured log stream (head-of-line blocking)")
	}
}

// recordBridge starts a bridge listener that flips *used on any connection; its
// presence proves non-hijack traffic never reaches the --publish-socket bridge.
func recordBridge(t *testing.T, cfg *config.Config) *bool {
	t.Helper()
	used := new(bool)
	ln, err := net.Listen("unix", dockerForwardSocketPath(cfg))
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
			*used = true
			_ = c.Close()
		}
	}()
	return used
}

// startHOLTrapBridge starts a bridge that emulates Apple's head-of-line blocking:
// it handles connections one at a time (no per-connection goroutine), so an
// undrained stream on the first connection prevents the next from being served.
// Correct routing never sends non-hijack traffic here, so it stays idle.
func startHOLTrapBridge(t *testing.T, cfg *config.Config) {
	t.Helper()
	ln, err := net.Listen("unix", dockerForwardSocketPath(cfg))
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
			// handled inline (NOT in a goroutine) → head-of-line blocking
			func() {
				defer c.Close()
				br := bufio.NewReader(c)
				_, _ = br.ReadString('\n') // dockerfwd header
				head, err := readHead(br)
				if err != nil {
					return
				}
				if strings.Contains(head, "/logs") {
					// write a large body and block until the peer drains it —
					// which, for an undrained client, never happens: HOL.
					_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 1000000\r\n\r\n")
					big := make([]byte, 1000000)
					_, _ = c.Write(big)
					return
				}
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 6\r\n\r\nBRIDGE")
			}()
		}
	}()
}
