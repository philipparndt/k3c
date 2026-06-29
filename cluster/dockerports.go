package cluster

import (
	"encoding/json"
	"fmt"
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

// startDockerSocket serves a host unix socket that forwards to the sidecar's
// docker engine via the stable Apple-published loopback endpoint
// (127.0.0.1:<DockerPort>), so it keeps working across sidecar recreation and
// when the guest vmnet IP is unreachable; connections made while no sidecar is
// running fail to dial and are closed. Idle (just an unused listener) until
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
			go func() {
				// dial the stable host-local engine endpoint, never the guest
				// vmnet IP (not host-reachable); a stopped sidecar has no
				// published port, so the dial fails fast and we close.
				upstream, err := net.DialTimeout("tcp", dockerEngineEndpoint(cfg), connectTimeout)
				if err != nil {
					_ = conn.Close()
					return
				}
				splice(conn, upstream)
			}()
		}
	}()
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
