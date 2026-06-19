// Package web is the browser front-end of k3c (k3c web): a local HTTP server
// that serves an animated data-flow diagram of the system and a read-only JSON
// state endpoint. It is an alternative to the terminal UI (k3c ui) and reuses
// the same read-only status accessors from the cluster package.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/cluster"
	"k3c/config"
)

// dist holds the built Preact/Vite front-end. Build it with `npm --prefix web
// run build` (committed so `go build` works without a node toolchain).
//
//go:embed all:dist
var dist embed.FS

// Options configures the web server.
type Options struct {
	Addr string // bind address, default 127.0.0.1
	Port int    // listen port; 0 (or a busy port) picks a free one
	Open bool   // open the URL in the browser
}

// Server serves the web UI and the state endpoint. It retains the last traffic
// sample per cluster so it can compute a rate across polls.
type Server struct {
	cfg  *config.Config
	mu   sync.Mutex
	last map[string]sample
}

type sample struct {
	rx, tx int64
	at     time.Time
}

// --- JSON state ---

type listenerJSON struct {
	Name   string `json:"name"`
	Port   string `json:"port"`
	Detail string `json:"detail"`
	Up     bool   `json:"up"`
}

type daemonJSON struct {
	State     string         `json:"state"`
	Pid       string         `json:"pid"`
	Listeners []listenerJSON `json:"listeners"`
}

type machineJSON struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // "" cluster, "docker" sidecar
	State   string `json:"state"`
	RAM     string `json:"ram"`
	Context string `json:"context"`
	Active  bool   `json:"active"`
	RxRate  int64  `json:"rxRate"`
	TxRate  int64  `json:"txRate"`
	HasRate bool   `json:"hasRate"`
}

type cacheJSON struct {
	Enabled   bool  `json:"enabled"`
	HitPct    int   `json:"hitPct"`
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	HitBytes  int64 `json:"hitBytes"`
	MissBytes int64 `json:"missBytes"`
}

type netJSON struct {
	Cluster string `json:"cluster"`
	RxRate  int64  `json:"rxRate"`
	TxRate  int64  `json:"txRate"`
	HasRate bool   `json:"hasRate"`
}

type stateJSON struct {
	Daemon   daemonJSON    `json:"daemon"`
	Machines []machineJSON `json:"machines"`
	Cache    cacheJSON     `json:"cache"`
	Net      netJSON       `json:"net"`
}

// collectState aggregates the current system state from the read-only cluster
// accessors. It mutates only the retained traffic samples.
func (s *Server) collectState() stateJSON {
	d := cluster.DaemonsState(s.cfg)
	dj := daemonJSON{State: d.State, Pid: d.Pid}
	for _, l := range d.Listeners {
		dj.Listeners = append(dj.Listeners, listenerJSON{Name: l.Name, Port: l.Port, Detail: l.Detail, Up: l.Up})
	}

	machines := cluster.Clusters(s.cfg)
	if sidecar, ok := cluster.DockerSidecarInfo(s.cfg); ok {
		machines = append(machines, sidecar)
	}
	active := cluster.ActiveClusterName()
	mj := make([]machineJSON, 0, len(machines))
	var netLine netJSON
	for _, c := range machines {
		m := machineJSON{
			Name: c.Name, Kind: c.Kind, State: c.Server,
			RAM: c.RAM, Context: c.Context, Active: c.Active,
		}
		// Sample every running machine so each VM's own activity (a cluster pod
		// pulling, or a docker-sidecar image pull) drives its edge — not just
		// the active cluster.
		if c.Server == "running" {
			if rate, ok := s.rate(c.Name, c.Kind); ok {
				m.RxRate, m.TxRate, m.HasRate = rate.rx, rate.tx, true
			}
		}
		mj = append(mj, m)
		if c.Name == active && c.Kind != "docker" {
			netLine = netJSON{Cluster: c.Name, RxRate: m.RxRate, TxRate: m.TxRate, HasRate: m.HasRate}
		}
	}

	cache := cacheJSON{}
	if s.cfg.PullCacheEnabled {
		if st, err := cluster.PullCacheStats(s.cfg); err == nil && st != nil {
			cache.Enabled = true
			cache.Hits, cache.Misses = st.Hits, st.Misses
			cache.HitBytes, cache.MissBytes = st.HitBytes, st.MissBytes
			if st.Hits+st.Misses > 0 {
				cache.HitPct = int(float64(st.Hits) * 100 / float64(st.Hits+st.Misses))
			}
		}
	}

	return stateJSON{Daemon: dj, Machines: mj, Cache: cache, Net: netLine}
}

type rateSample struct{ rx, tx int64 }

// rate samples a machine's traffic and returns the per-second rate computed
// against the previously retained sample, skipping a sample whose counters
// reset (a VM restart) or the first sample (no baseline yet).
func (s *Server) rate(name, kind string) (rateSample, bool) {
	rx, tx, err := cluster.MachineTraffic(s.cfg, name, kind)
	if err != nil {
		return rateSample{}, false
	}
	now := time.Now()
	s.mu.Lock()
	prev, ok := s.last[name]
	s.last[name] = sample{rx: rx, tx: tx, at: now}
	s.mu.Unlock()
	if !ok {
		return rateSample{}, false
	}
	el := now.Sub(prev.at).Seconds()
	if el <= 0 || rx < prev.rx || tx < prev.tx {
		return rateSample{}, false
	}
	return rateSample{rx: int64(float64(rx-prev.rx) / el), tx: int64(float64(tx-prev.tx) / el)}, true
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(s.collectState())
}

// actionArgs maps a (kind, action) pair to the k3c CLI arguments that perform
// it, or nil when the pair is not allowed. play/pause/stop are expressed as
// start/resume/pause/stop; the front-end resolves "play" to start or resume.
func actionArgs(kind, name, action string) []string {
	if kind == "docker" {
		switch action {
		case "start":
			return []string{"docker", "up"}
		case "stop":
			return []string{"docker", "down"}
		case "pause":
			return []string{"docker", "pause"}
		case "resume":
			return []string{"docker", "resume"}
		}
		return nil
	}
	switch action {
	case "start", "stop", "pause", "resume":
		return []string{"cluster", action, name}
	}
	return nil
}

type actionReq struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Action string `json:"action"`
}

// handleAction runs a lifecycle action against a known machine by executing the
// k3c binary, the same way the TUI does. It rejects unknown machines and
// actions so the request cannot be turned into an arbitrary command.
func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req actionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.knownMachine(req.Name, req.Kind) {
		http.Error(w, "unknown machine", http.StatusBadRequest)
		return
	}
	args := actionArgs(req.Kind, req.Name, req.Action)
	if args == nil {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	logger.Info(fmt.Sprintf("web action: %s %s (%s)", req.Action, req.Name, req.Kind))
	out, err := runK3c(args...)
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{"ok": err == nil, "output": out}
	if err != nil {
		resp["error"] = err.Error()
		w.WriteHeader(http.StatusInternalServerError)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// knownMachine reports whether name/kind matches a currently listed machine, so
// actions cannot target arbitrary names.
func (s *Server) knownMachine(name, kind string) bool {
	if kind == "docker" {
		sidecar, ok := cluster.DockerSidecarInfo(s.cfg)
		return ok && sidecar.Name == name
	}
	for _, c := range cluster.Clusters(s.cfg) {
		if c.Name == name {
			return true
		}
	}
	return false
}

// runK3c executes the running binary with args and returns the combined output.
func runK3c(args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(exe, args...).CombinedOutput()
	return string(out), err
}

// Serve starts the web server, opening the browser unless suppressed. It
// returns when the server stops.
func Serve(cfg *config.Config, opts Options) error {
	s := &Server{cfg: cfg, last: map[string]sample{}}

	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/action", s.handleAction)
	mux.Handle("/", http.FileServer(http.FS(sub)))

	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1"
	}
	ln, err := listen(addr, opts.Port)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String()
	logger.Info("k3c web serving at " + url)
	fmt.Println(url)
	if opts.Open {
		openBrowser(url)
	}
	return http.Serve(ln, mux)
}

// listen binds the requested port, falling back to a free one when it is busy.
func listen(addr string, port int) (net.Listener, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", addr, port))
	if err != nil && port != 0 {
		logger.Info(fmt.Sprintf("port %d unavailable, picking a free one", port))
		return net.Listen("tcp", addr+":0")
	}
	return ln, err
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	if err := exec.Command(cmd, append(args, url)...).Start(); err != nil {
		logger.Warn("could not open browser: " + err.Error())
	}
}
