package cluster

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

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
	go func() {
		active := map[string]net.Listener{}
		for {
			reconcileDockerPorts(active)
			time.Sleep(dockerPortPoll)
		}
	}()
}

// reconcileDockerPorts brings the set of host listeners in line with the
// sidecar's currently published ports. Listeners are keyed by host:port so
// the same port published on different addresses is tracked independently.
func reconcileDockerPorts(active map[string]net.Listener) {
	ip := containerIP(dockerName)
	desired := map[string]portBind{}
	if ip != "" {
		for _, b := range dockerPublishedPorts(ip) {
			desired[b.addr()] = b
		}
	}

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
		// dial the sidecar's own published port (always on the VM)
		go acceptDockerForward(ln, fmt.Sprintf("%s:%d", ip, b.port))
	}

	for key, ln := range active {
		if _, ok := desired[key]; !ok {
			_ = ln.Close()
			delete(active, key)
			logger.Info(fmt.Sprintf("docker: stopped forwarding %s", key))
		}
	}
}

func acceptDockerForward(ln net.Listener, target string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed by reconcile
		}
		go func() {
			upstream, err := net.DialTimeout("tcp", target, connectTimeout)
			if err != nil {
				conn.Close()
				return
			}
			splice(conn, upstream)
		}()
	}
}

// dockerPublishedPorts returns the published host TCP endpoints reported by
// the sidecar's docker engine, preserving the bind address docker chose.
func dockerPublishedPorts(ip string) []portBind {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + ip + ":2375/containers/json")
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
