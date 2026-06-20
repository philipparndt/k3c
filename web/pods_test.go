package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k3c/cluster"
)

// handlePods is read-only and returns an empty list (never nil/error) when no
// cluster is targetable, so the browser can render an empty state.
func TestHandlePodsEmptyWhenNoCluster(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/pods", nil)
	rec := httptest.NewRecorder()
	s.handlePods(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Cluster string            `json:"cluster"`
		Pods    []cluster.PodInfo `json:"pods"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pods == nil {
		t.Error("pods is null, want [] so the client can render an empty list")
	}
}

func TestTargetClusterUsesQueryParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/pods?cluster=demo", nil)
	if got := targetCluster(req); got != "demo" {
		t.Errorf("targetCluster = %q, want demo", got)
	}
}

// The SSE endpoint refuses a request that resolves to no cluster rather than
// opening an empty stream.
func TestHandlePodsStreamNoCluster(t *testing.T) {
	s := &Server{pods: newPodStreamManager(context.Background())}
	req := httptest.NewRequest(http.MethodGet, "/api/pods/stream", nil)
	rec := httptest.NewRecorder()
	s.handlePodsStream(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// fanOut broadcasts each tick to subscribers and, when the source closes,
// closes every subscriber channel and removes the stream from the manager.
func TestPodStreamFanOutAndCleanup(t *testing.T) {
	mgr := newPodStreamManager(context.Background())
	src := make(chan cluster.Snapshot)
	ps := &podStream{mgr: mgr, cluster: "demo", cancel: func() {}, subs: map[chan cluster.Snapshot]struct{}{}}
	mgr.streams["demo"] = ps

	sub := make(chan cluster.Snapshot, 1)
	ps.subs[sub] = struct{}{}
	go ps.fanOut(src)

	src <- cluster.Snapshot{TimeMillis: 1, Pods: map[string]cluster.PodSample{"u": {CPUUsec: 7}}}
	select {
	case snap := <-sub:
		if snap.TimeMillis != 1 {
			t.Errorf("got t_ms %d, want 1", snap.TimeMillis)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the tick")
	}

	close(src) // source ends (cluster stopped / shutdown)

	select {
	case _, ok := <-sub:
		// drain any buffered tick, then expect a close
		if ok {
			if _, ok = <-sub; ok {
				t.Error("subscriber channel not closed after source ended")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber channel not closed after source ended")
	}

	mgr.mu.Lock()
	_, present := mgr.streams["demo"]
	mgr.mu.Unlock()
	if present {
		t.Error("stream not removed from manager after source ended")
	}
}
