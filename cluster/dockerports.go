package cluster

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
	"k3c/dockerfwd"
)

// dockerSocketPath is the host-side unix socket the daemon publishes for the
// sidecar's docker engine. Unlike the sidecar's VM IP (which changes on every
// recreate), this path is stable, so the docker context and DOCKER_HOST can
// point at it for the lifetime of the install — the way Docker Desktop and
// similar tools expose a local engine socket.
func dockerSocketPath(cfg *config.Config) string {
	return filepath.Join(cfg.BaseDir, "docker.sock")
}

// dockerEngineEndpoint is the stable, host-local address the Apple runtime
// publishes the sidecar engine on (docker.go: -p 127.0.0.1:<DockerPort>:2375).
// Forwarding through this loopback endpoint never depends on the guest vmnet IP
// being reachable from the host at L2 — which it is not (see the
// docker-sidecar-host-forwarder change, OQ#2). It survives sidecar recreation:
// the runtime republishes the same host port.
func dockerEngineEndpoint(cfg *config.Config) string {
	return net.JoinHostPort("127.0.0.1", cfg.DockerPort)
}

// guestForwardSocket is where the in-guest forwarder listens; --publish-socket
// bridges dockerForwardSocketPath(host) to this path inside the VM.
const guestForwardSocket = "/run/k3c-docker-fwd.sock"

// guestEnginePort is the port dockerd listens on inside the sidecar VM
// (docker.go launches it with --host=tcp://0.0.0.0:2375). The in-guest
// forwarder dials it on the guest loopback, so the engine reaches the host
// over the same full-duplex unix bridge as nested ports.
const guestEnginePort = 2375

// sidecarTargetPrefix marks a dial target served by the sidecar's nested-port
// forwarder (vs. a plain host:port dialed over tcp). The "|" cannot appear in a
// hostname nor in net.JoinHostPort output, so a sidecar target can never be
// confused with a real host:port — e.g. an attacker-chosen SNI "sidecar" in the
// :443 egress path becomes "sidecar:443", which is NOT this prefix.
const sidecarTargetPrefix = "sidecar|"

// dockerForwardSocketPath is the host-side unix socket the Apple runtime bridges
// (via --publish-socket) to the in-guest forwarder. The host dials it to reach
// any nested published container port through the forwarder, never the guest
// vmnet IP. Stable for the install lifetime, like dockerSocketPath.
func dockerForwardSocketPath(cfg *config.Config) string {
	return filepath.Join(cfg.BaseDir, "docker-fwd.sock")
}

// dialSidecarPort opens a connection to a nested published port on the sidecar
// VM through the in-guest forwarder over the published unix socket — with no
// guest vmnet L2 dependency. The caller splices the returned conn.
func dialSidecarPort(cfg *config.Config, port int) (net.Conn, error) {
	c, err := net.DialTimeout("unix", dockerForwardSocketPath(cfg), connectTimeout)
	if err != nil {
		return nil, err
	}
	if err := dockerfwd.WriteHeader(c, port); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// dialTarget dials an arbitration/forwarding target: a "sidecar:<port>" target
// goes through the in-guest forwarder (unix socket); anything else is a plain
// tcp host:port. This is the single place the sidecar data plane is chosen, so
// every contested-port and nested-port path stays off the guest vmnet IP.
func dialTarget(cfg *config.Config, target string) (net.Conn, error) {
	if p, ok := strings.CutPrefix(target, sidecarTargetPrefix); ok {
		port, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("bad sidecar target %q: %w", target, err)
		}
		return dialSidecarPort(cfg, port)
	}
	return net.DialTimeout("tcp", target, connectTimeout)
}

// dialDockerEngine opens a connection to the sidecar's docker engine over the
// in-guest forwarder (the --publish-socket bridge) — full-duplex, so Docker's
// hijacked streams (exec, attach, interactive run) survive, with no guest vmnet
// L2 dependency. It falls back to the Apple-published loopback endpoint
// (127.0.0.1:<DockerPort>) when the bridge is absent — a sidecar created before
// --publish-socket: the engine still answers plain request/response there, but
// hijacked streams are dropped by Apple's TCP publish, so such a sidecar should
// be recreated to regain exec/attach. This is the transport for hijacked and
// unrecognized engine traffic; non-hijacked traffic prefers the loopback
// endpoint (routeEngineConn) because the bridge head-of-line-blocks a
// back-pressured stream (see the docker-engine-api-stream-hol-blocking change).
func dialDockerEngine(cfg *config.Config) (net.Conn, error) {
	if c, err := dialSidecarPort(cfg, guestEnginePort); err == nil {
		return c, nil
	}
	return net.DialTimeout("tcp", dockerEngineEndpoint(cfg), connectTimeout)
}

// engineHeadPeekMax bounds how many leading bytes routeEngineConn inspects to
// find the end of the HTTP request head. Well over a normal Docker request head;
// anything larger is treated as unrecognized and routed to the bridge.
const engineHeadPeekMax = 64 << 10

// startDockerSocket serves a host unix socket that forwards to the sidecar's
// docker engine. Each accepted connection is routed by request type
// (routeEngineConn): hijacked/interactive streams go over the full-duplex
// --publish-socket bridge, everything else over the Apple-published loopback
// endpoint, which — unlike the bridge — does not head-of-line-block a
// back-pressured streaming response. It keeps working across sidecar recreation
// and when the guest vmnet IP is unreachable; connections made while no sidecar
// is running fail to dial and are closed. Idle (just an unused listener) until
// something dials it.
func startDockerSocket(cfg *config.Config) {
	path := dockerSocketPath(cfg)
	// a stale socket file from a previous daemon blocks the bind
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		logger.Warn("docker socket: " + err.Error())
		return
	}
	_ = os.Chmod(path, 0o600)
	logger.Info("docker: engine socket at " + path)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go routeEngineConn(cfg, conn)
		}
	}()
}

// routeEngineConn forwards one host connection to the sidecar's docker engine,
// choosing the transport by peeking the first HTTP request head:
//
//   - A clearly non-hijacked request (inspect, logs, wait, events, build,
//     archive PUT / `docker cp`, …) is spliced to the Apple-published loopback
//     endpoint, which handles a back-pressured stream + concurrent request
//     without head-of-line blocking (proven; the bridge does not).
//   - A hijacked/upgrade stream (`docker exec`/`attach`, interactive run), or any
//     head we cannot confidently classify, is spliced over the full-duplex
//     --publish-socket bridge (dialDockerEngine), which carries hijacks; Apple's
//     loopback publish drops them.
//
// Routing the whole connection by its FIRST request is sound: the moby client
// dials a dedicated, unpooled connection for every hijack and never reuses the
// upgraded connection, so a pooled keep-alive connection only ever carries
// non-hijack requests — a connection's first request type is its type for life.
func routeEngineConn(cfg *config.Config, conn net.Conn) {
	br := bufio.NewReaderSize(conn, engineHeadPeekMax)
	// Bound the head read so a client that connects but never sends a request
	// can't pin this goroutine; cleared before splicing the stream.
	_ = conn.SetReadDeadline(time.Now().Add(connectTimeout))
	head, parsed, hijack := readEngineHead(br)
	_ = conn.SetReadDeadline(time.Time{})

	// Only divert to loopback when we are SURE it is a non-hijack HTTP request;
	// hijacks and anything unrecognized keep today's bridge-first behavior.
	if parsed && !hijack {
		if upstream, err := net.DialTimeout("tcp", dockerEngineEndpoint(cfg), connectTimeout); err == nil {
			spliceEngine(conn, head, br, upstream)
			return
		}
		// loopback unexpectedly unavailable — fall through to the bridge
	}
	upstream, err := dialDockerEngine(cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	spliceEngine(conn, head, br, upstream)
}

// readEngineHead consumes the leading HTTP request head from br (up to and
// including the blank line that ends it), returning the exact head bytes so the
// caller can replay them to the chosen upstream, plus whether they parse as an
// HTTP request and, if so, whether it is a hijacked/upgrade stream. A read error
// or an oversized head yields the bytes read so far and parsed=false (→ bridge).
//
// Hijack signals (either is sufficient): a `Connection: Upgrade` / `Upgrade:`
// request header (moby sets `Connection: Upgrade` + `Upgrade: tcp` for hijacks),
// or a hijack request path (`/exec/{id}/start`, `.../attach`, `.../attach/ws`).
func readEngineHead(br *bufio.Reader) (head []byte, parsed, hijack bool) {
	var buf bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		buf.WriteString(line)
		if err != nil || buf.Len() > engineHeadPeekMax {
			return buf.Bytes(), false, false // short/half-open, non-HTTP, or oversized
		}
		if line == "\r\n" || line == "\n" { // blank line ends the head
			parsed, hijack = classifyEngineHead(buf.Bytes())
			return buf.Bytes(), parsed, hijack
		}
	}
}

// classifyEngineHead inspects an HTTP request head (request line + headers, no
// trailing CRLFCRLF) and reports (parsed, hijack).
func classifyEngineHead(head []byte) (parsed, hijack bool) {
	lines := strings.Split(string(head), "\r\n")
	if len(lines) == 0 {
		return false, false
	}
	// Request line: METHOD SP REQUEST-URI SP HTTP/x.y
	fields := strings.Fields(lines[0])
	if len(fields) < 3 || !strings.HasPrefix(fields[2], "HTTP/") {
		return false, false // not an HTTP request head
	}
	uri := fields[1]
	if strings.Contains(uri, "/attach") ||
		(strings.Contains(uri, "/exec/") && strings.Contains(uri, "/start")) {
		return true, true
	}
	for _, ln := range lines[1:] {
		name, val, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "upgrade":
			return true, true
		case "connection":
			if strings.Contains(strings.ToLower(val), "upgrade") {
				return true, true
			}
		}
	}
	return true, false
}

// spliceEngine copies bidirectionally between a host engine connection and
// upstream, after first replaying head (the request head already consumed from
// the client for routing) to upstream. The client→upstream side then continues
// from cr (the buffered reader, which may still hold body/pipelined bytes). Each
// write side is half-closed on EOF so hijacked half-duplex streams are not torn
// down early (mirroring splice).
func spliceEngine(conn net.Conn, head []byte, cr io.Reader, upstream net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		if len(head) > 0 {
			if _, err := upstream.Write(head); err != nil {
				closeWrite(upstream)
				done <- struct{}{}
				return
			}
		}
		_, _ = io.Copy(upstream, cr)
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstream)
		closeWrite(conn)
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = conn.Close()
	_ = upstream.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// sidecarPorts maps every host port the sidecar currently publishes to its
// "sidecar:<port>" dial target (resolved by dialTarget through the in-guest
// forwarder). The daemon reads it to route contested ports (a port both it and
// the sidecar serve) to the sidecar when the sidecar is the active target —
// including :443 ingress for a nested k3d cluster whose ingress lives on the
// sidecar VM. Refreshed by the port poll below.
var sidecarPorts atomic.Pointer[map[int]string]

// sidecarPortTarget returns the sidecar dial target for the host port, or "".
func sidecarPortTarget(port int) string {
	if m := sidecarPorts.Load(); m != nil {
		return (*m)[port]
	}
	return ""
}

func storeSidecarPorts(m map[int]string) {
	sidecarPorts.Store(&m)
}

// Docker published-port forwarding. Docker publishes container ports on
// the sidecar VM's network (e.g. 0.0.0.0:65270 inside the VM), not on the
// host — so `docker run -p`, docker-compose, and tools that assume
// localhost publishing (k3d, many test harnesses) cannot reach them. This
// watcher polls the engine and mirrors every published TCP port onto the
// host, the way Docker Desktop's port forwarder does. It honors the bind
// address docker reports (`-p 0.0.0.0:x` → host 0.0.0.0, so 127.0.0.2 and
// other loopback aliases work; `-p 127.0.0.1:x` → host 127.0.0.1), which is
// what tools like k3d that point a kubeconfig at 127.0.0.x rely on. It runs
// in the daemons, idle until the sidecar appears.

const dockerPortPoll = 5 * time.Second

// portBind is a single published TCP endpoint: the host address docker
// publishes on, and the port.
type portBind struct {
	host string
	port int
}

func (b portBind) addr() string { return net.JoinHostPort(b.host, strconv.Itoa(b.port)) }

func startDockerPortForward(cfg *config.Config) {
	owned := daemonHostPorts(cfg)
	go func() {
		active := map[string]net.Listener{}
		for {
			reconcileDockerPorts(cfg, active, owned)
			time.Sleep(dockerPortPoll)
		}
	}()
}

// reconcileDockerPorts brings the set of host listeners in line with the
// sidecar's currently published ports, and records every published port for
// the daemon's contested-port arbitration. Listeners are keyed by host:port so
// the same port published on different addresses is tracked independently.
func reconcileDockerPorts(cfg *config.Config, active map[string]net.Listener, owned map[int]bool) {
	// Discovery reads the engine over the stable loopback endpoint, and the data
	// plane reaches each published port through the in-guest forwarder over the
	// published unix socket (dialTarget → dialSidecarPort) — both independent of
	// the guest vmnet IP being host-reachable.
	desired := map[string]portBind{}
	published := map[int]string{}
	for _, b := range dockerPublishedPorts(dockerEngineEndpoint(cfg)) {
		published[b.port] = fmt.Sprintf("%s%d", sidecarTargetPrefix, b.port)
		// daemon-owned ports (:443 ingress, registry, proxy, egress,
		// pull-cache) are contested: the daemon already holds the host bind and
		// its arbitration wrapper routes them to the sidecar when the sidecar is
		// the active target. Don't also bind them here.
		if owned[b.port] {
			continue
		}
		desired[b.addr()] = b
	}
	storeSidecarPorts(published)

	for key, b := range desired {
		if _, ok := active[key]; ok {
			continue
		}
		ln, err := net.Listen("tcp", key)
		if err != nil {
			// port taken on the host (or transient): retry next cycle
			continue
		}
		active[key] = ln
		logger.Info(fmt.Sprintf("docker: forwarding %s -> sidecar", key))
		go acceptDockerForward(cfg, ln, b.port)
	}

	for key, ln := range active {
		if _, ok := desired[key]; !ok {
			_ = ln.Close()
			delete(active, key)
			logger.Info(fmt.Sprintf("docker: stopped forwarding %s", key))
		}
	}
}

func acceptDockerForward(cfg *config.Config, ln net.Listener, port int) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed by reconcile
		}
		go func() {
			upstream, err := dialSidecarPort(cfg, port)
			if err != nil {
				conn.Close()
				return
			}
			splice(conn, upstream)
		}()
	}
}

// dockerPublishedPorts returns the published host TCP endpoints reported by
// the sidecar's docker engine, preserving the bind address docker chose. It
// queries the engine over the stable loopback endpoint (127.0.0.1:<DockerPort>),
// so discovery does not depend on the guest vmnet IP being host-reachable.
func dockerPublishedPorts(endpoint string) []portBind {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + endpoint + "/containers/json")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var containers []struct {
		Ports []struct {
			IP         string `json:"IP"`
			PublicPort int    `json:"PublicPort"`
			Type       string `json:"Type"`
		} `json:"Ports"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var binds []portBind
	for _, c := range containers {
		for _, p := range c.Ports {
			if p.Type != "tcp" || p.PublicPort == 0 {
				continue
			}
			// docker reports a 0.0.0.0 publish as both 0.0.0.0 and ::;
			// the IPv4 listener already serves every local address, so
			// skip the IPv6 twin to avoid a redundant (and clashing) bind.
			if strings.Contains(p.IP, ":") {
				continue
			}
			host := p.IP
			if host == "" {
				host = "0.0.0.0"
			}
			b := portBind{host: host, port: p.PublicPort}
			if seen[b.addr()] {
				continue
			}
			seen[b.addr()] = true
			binds = append(binds, b)
		}
	}
	return binds
}
