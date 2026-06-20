package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/cluster"
	"k3c/config"
)

// webProfileInterval is the sampling cadence for the browser pod stream. It is
// a touch slower than the CLI default (500ms) because the sparklines and
// heatmaps read more smoothly at ~1s and it keeps node load light.
const webProfileInterval = time.Second

// podStreamManager runs at most one profiler per cluster, shared across every
// connected browser. The first subscriber starts the profiler; the last to
// leave cancels it (which kills the node sampler via ProfileStream's
// kill-on-cancel path). A base context, cancelled on server shutdown, bounds
// every stream so nothing outlives the server.
type podStreamManager struct {
	baseCtx context.Context
	mu      sync.Mutex
	streams map[string]*podStream
}

type podStream struct {
	mgr     *podStreamManager
	cluster string
	cancel  context.CancelFunc

	mu   sync.Mutex
	subs map[chan cluster.Snapshot]struct{}
	last *cluster.Snapshot // most recent tick, replayed to a new subscriber
}

func newPodStreamManager(ctx context.Context) *podStreamManager {
	return &podStreamManager{baseCtx: ctx, streams: map[string]*podStream{}}
}

// subscribe attaches a new consumer to the cluster's shared profiler, starting
// it on the first subscriber. It returns the consumer channel and an unsubscribe
// function the caller MUST invoke when it is done.
func (m *podStreamManager) subscribe(clusterName string) (<-chan cluster.Snapshot, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps, ok := m.streams[clusterName]
	if !ok {
		cfg, err := config.Resolve(clusterName, "")
		if err != nil {
			return nil, nil, fmt.Errorf("resolving cluster %q: %w", clusterName, err)
		}
		ctx, cancel := context.WithCancel(m.baseCtx)
		// Resolve UID -> namespace/name so the browser shows real pod names.
		src, err := cluster.ProfileStream(ctx, cfg, webProfileInterval, true)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		ps = &podStream{mgr: m, cluster: clusterName, cancel: cancel, subs: map[chan cluster.Snapshot]struct{}{}}
		m.streams[clusterName] = ps
		go ps.fanOut(src)
	}

	ch := make(chan cluster.Snapshot, 1)
	ps.mu.Lock()
	ps.subs[ch] = struct{}{}
	if ps.last != nil {
		ch <- *ps.last // give the newcomer something to render immediately
	}
	ps.mu.Unlock()

	return ch, func() { m.unsubscribe(clusterName, ch) }, nil
}

func (m *podStreamManager) unsubscribe(clusterName string, ch chan cluster.Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps, ok := m.streams[clusterName]
	if !ok {
		return
	}
	ps.mu.Lock()
	if _, ok := ps.subs[ch]; ok {
		delete(ps.subs, ch)
		close(ch)
	}
	empty := len(ps.subs) == 0
	ps.mu.Unlock()
	if empty {
		ps.cancel() // last viewer left — stop the node sampler
		delete(m.streams, clusterName)
	}
}

// fanOut broadcasts each tick to every subscriber and tears the stream down when
// the source closes (profiler cancelled, or the cluster stopped mid-stream).
func (ps *podStream) fanOut(src <-chan cluster.Snapshot) {
	for snap := range src {
		ps.mu.Lock()
		s := snap
		ps.last = &s
		for ch := range ps.subs {
			select {
			case ch <- snap:
			default: // a slow consumer drops this tick rather than stalling all
			}
		}
		ps.mu.Unlock()
	}
	// Source ended: drop the stream from the manager and close every subscriber
	// so their SSE handlers return.
	ps.mgr.mu.Lock()
	if ps.mgr.streams[ps.cluster] == ps {
		delete(ps.mgr.streams, ps.cluster)
	}
	ps.mgr.mu.Unlock()
	ps.mu.Lock()
	for ch := range ps.subs {
		delete(ps.subs, ch)
		close(ch)
	}
	ps.mu.Unlock()
}

// targetCluster resolves the cluster a pods request targets: the "cluster"
// query parameter, or the active cluster when none is given.
func targetCluster(r *http.Request) string {
	if c := r.URL.Query().Get("cluster"); c != "" {
		return c
	}
	return cluster.ActiveClusterName()
}

// handlePods returns the current pod list for the target cluster. It is
// read-only: it lists pods from the API server and mutates nothing.
func (s *Server) handlePods(w http.ResponseWriter, r *http.Request) {
	name := targetCluster(r)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	var pods []cluster.PodInfo
	if name != "" {
		if cfg, err := config.Resolve(name, ""); err == nil {
			pods = cluster.PodList(cfg)
		}
	}
	if pods == nil {
		pods = []cluster.PodInfo{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"cluster": name, "pods": pods})
}

// handlePodsStream streams profiler ticks for the target cluster to the browser
// as Server-Sent Events, sharing one profiler across all connected clients.
func (s *Server) handlePodsStream(w http.ResponseWriter, r *http.Request) {
	name := targetCluster(r)
	if name == "" {
		http.Error(w, "no cluster", http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch, unsub, err := s.pods.subscribe(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	logger.Debug("web: pod stream opened for " + name)
	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case snap, ok := <-ch:
			if !ok {
				return // profiler ended (cluster stopped or shutdown)
			}
			if _, err := fmt.Fprint(w, "data: "); err != nil {
				return
			}
			if err := enc.Encode(snap); err != nil { // Encode writes the trailing newline
				return
			}
			if _, err := fmt.Fprint(w, "\n"); err != nil { // blank line terminates the SSE event
				return
			}
			flusher.Flush()
		}
	}
}
