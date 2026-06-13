package cluster

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// Docker published-port forwarding. Docker publishes container ports on
// the sidecar VM's network (e.g. 0.0.0.0:65270 inside the VM), not on the
// host — so `docker run -p`, docker-compose, and tools that assume
// localhost publishing (k3d, many test harnesses) cannot reach them. This
// watcher polls the engine and mirrors every published TCP port onto the
// host's 127.0.0.1, the way Docker Desktop's port forwarder does. It runs
// in the daemons, idle until the sidecar appears.

const dockerPortPoll = 5 * time.Second

func startDockerPortForward(cfg *config.Config) {
	go func() {
		active := map[int]net.Listener{}
		for {
			reconcileDockerPorts(active)
			time.Sleep(dockerPortPoll)
		}
	}()
}

// reconcileDockerPorts brings the set of host listeners in line with the
// sidecar's currently published ports.
func reconcileDockerPorts(active map[int]net.Listener) {
	ip := containerIP(dockerName)
	desired := map[int]bool{}
	if ip != "" {
		for _, p := range dockerPublishedPorts(ip) {
			desired[p] = true
		}
	}

	for port := range desired {
		if _, ok := active[port]; ok {
			continue
		}
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			// port taken on the host (or transient): retry next cycle
			continue
		}
		active[port] = ln
		logger.Info(fmt.Sprintf("docker: forwarding 127.0.0.1:%d -> sidecar", port))
		go acceptDockerForward(ln, fmt.Sprintf("%s:%d", ip, port))
	}

	for port, ln := range active {
		if !desired[port] {
			_ = ln.Close()
			delete(active, port)
			logger.Info(fmt.Sprintf("docker: stopped forwarding 127.0.0.1:%d", port))
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

// dockerPublishedPorts returns the published host TCP ports reported by the
// sidecar's docker engine.
func dockerPublishedPorts(ip string) []int {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + ip + ":2375/containers/json")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var containers []struct {
		Ports []struct {
			PublicPort int    `json:"PublicPort"`
			Type       string `json:"Type"`
		} `json:"Ports"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil
	}
	seen := map[int]bool{}
	var ports []int
	for _, c := range containers {
		for _, p := range c.Ports {
			if p.Type == "tcp" && p.PublicPort > 0 && !seen[p.PublicPort] {
				seen[p.PublicPort] = true
				ports = append(ports, p.PublicPort)
			}
		}
	}
	return ports
}
