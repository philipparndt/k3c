package cluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"k3c/config"
)

func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// newTestCache builds a pullCache backed by a temp content store whose only
// upstream is the given test server.
func newTestCache(t *testing.T, upstreamURL string) *pullCache {
	t.Helper()
	cfg := &config.Config{BaseDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(pullCacheDir(cfg), "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := newPullCache(cfg)
	p.upstreams = map[string][]string{"reg.example": {upstreamURL}}
	return p
}

// A cold GET streams the blob to the client and commits it to the cache; the
// next GET is served from disk without touching the upstream.
func TestServeBlobStreamsThenServesFromCache(t *testing.T) {
	blob := bytes.Repeat([]byte("layer-bytes-"), 4096) // ~48 KB
	digest := sha256Digest(blob)
	var upstreamHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
		_, _ = w.Write(blob)
	}))
	defer upstream.Close()

	p := newTestCache(t, upstream.URL)

	// cold miss → streamed and cached
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v2/img/blobs/"+digest, nil)
	p.serveBlob(rec, req, "reg.example", "img", digest)

	if rec.Code != http.StatusOK {
		t.Fatalf("cold status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), blob) {
		t.Error("streamed body does not match upstream blob")
	}
	if got, err := os.ReadFile(p.blobPath(digest)); err != nil || !bytes.Equal(got, blob) {
		t.Errorf("blob not committed to cache correctly: err=%v", err)
	}
	if p.misses != 1 || p.hits != 0 {
		t.Errorf("after cold miss: hits=%d misses=%d, want 0/1", p.hits, p.misses)
	}

	// warm hit → served from disk, no extra upstream fetch
	rec2 := httptest.NewRecorder()
	p.serveBlob(rec2, httptest.NewRequest(http.MethodGet, "/v2/img/blobs/"+digest, nil), "reg.example", "img", digest)
	if rec2.Code != http.StatusOK || !bytes.Equal(rec2.Body.Bytes(), blob) {
		t.Error("warm serve failed")
	}
	if p.hits != 1 {
		t.Errorf("after warm hit: hits=%d, want 1", p.hits)
	}
	if n := atomic.LoadInt32(&upstreamHits); n != 1 {
		t.Errorf("upstream fetched %d times, want exactly 1 (warm hit must not refetch)", n)
	}
}

// If the upstream content does not match the requested digest, the bytes are
// still streamed (containerd is the backstop), but the corrupt content is NOT
// committed to the cache.
func TestServeBlobDigestMismatchNotCached(t *testing.T) {
	claimed := sha256Digest([]byte("the real content"))
	wrong := []byte("a different payload entirely")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(wrong)))
		_, _ = w.Write(wrong)
	}))
	defer upstream.Close()

	p := newTestCache(t, upstream.URL)
	rec := httptest.NewRecorder()
	p.serveBlob(rec, httptest.NewRequest(http.MethodGet, "/v2/img/blobs/"+claimed, nil), "reg.example", "img", claimed)

	if _, err := os.Stat(p.blobPath(claimed)); err == nil {
		t.Error("corrupt content was cached; it must not be")
	}
	if p.misses != 0 {
		t.Errorf("mismatch must not count as a served miss: misses=%d", p.misses)
	}
	// no leftover temp files in the blobs dir
	entries, _ := os.ReadDir(filepath.Join(pullCacheDir(p.cfg), "blobs"))
	if len(entries) != 0 {
		t.Errorf("blobs dir not clean after mismatch: %d entries", len(entries))
	}
}

// TestColdPullTimingStreamingVsStoreAndForward drives both the new streaming
// path and the original store-and-forward path through the real cache handler,
// with a throttled upstream and a throttled "node" reader standing in for the
// upstream-over-proxy and host->node vmnet legs. It demonstrates that streaming
// overlaps the two transfers (much lower time-to-first-byte and lower total).
func TestColdPullTimingStreamingVsStoreAndForward(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test sleeps to simulate transfer pacing")
	}
	const (
		chunk    = 512 << 10             // 512 KB
		chunks   = 24                    // -> 12 MB blob
		legDelay = 12 * time.Millisecond // per-chunk pacing for each leg
	)
	blob := bytes.Repeat([]byte("k3c-pull-cache-streaming-demo!!!"), (chunk*chunks)/32)
	digest := sha256Digest(blob)

	// upstream serves the blob throttled, to mimic fetching over a slow proxy.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
		fl, _ := w.(http.Flusher)
		for off := 0; off < len(blob); off += chunk {
			end := min(off+chunk, len(blob))
			_, _ = w.Write(blob[off:end])
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(legDelay)
		}
	}))
	defer upstream.Close()

	// run issues the request and consumes the body at the node's pace, timing
	// everything from when the request is sent — so time-to-first-byte captures
	// the store-and-forward path's wait for the full upstream download (its
	// headers only arrive once the whole blob has landed on the host).
	run := func(handler http.HandlerFunc) (ttfb, total time.Duration, n int) {
		srv := httptest.NewServer(handler)
		defer srv.Close()
		start := time.Now()
		resp, err := http.Get(srv.URL + "/v2/img/blobs/" + digest)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		buf := make([]byte, chunk)
		for {
			m, err := resp.Body.Read(buf)
			if m > 0 {
				if n == 0 {
					ttfb = time.Since(start)
				}
				n += m
				time.Sleep(legDelay) // host->node vmnet pacing
			}
			if err != nil {
				break
			}
		}
		return ttfb, time.Since(start), n
	}

	// NEW: streaming path (cold cache).
	pNew := newTestCache(t, upstream.URL)
	newTTFB, newTotal, newN := run(func(w http.ResponseWriter, r *http.Request) {
		pNew.serveBlob(w, r, "reg.example", "img", digest)
	})

	// OLD: store-and-forward — full upstream download, then serve from disk.
	pOld := newTestCache(t, upstream.URL)
	oldTTFB, oldTotal, oldN := run(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(pOld.blobPath(digest)); err != nil {
			if err := pOld.fetchContent("reg.example", "img", "blobs", digest, digest, ""); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
		pOld.serveFile(w, r, pOld.blobPath(digest), digest, "application/octet-stream")
	})

	if newN != len(blob) || oldN != len(blob) {
		t.Fatalf("short read: new=%d old=%d want=%d", newN, oldN, len(blob))
	}
	t.Logf("blob=%d KB, per-leg pacing=%v", len(blob)>>10, legDelay)
	t.Logf("store-and-forward: TTFB=%v  total=%v", oldTTFB.Round(time.Millisecond), oldTotal.Round(time.Millisecond))
	t.Logf("streaming:         TTFB=%v  total=%v", newTTFB.Round(time.Millisecond), newTotal.Round(time.Millisecond))
	t.Logf("→ TTFB %.1fx faster, total %.1fx faster",
		float64(oldTTFB)/float64(newTTFB), float64(oldTotal)/float64(newTotal))

	if newTTFB >= oldTTFB {
		t.Errorf("streaming TTFB (%v) should be well below store-and-forward (%v)", newTTFB, oldTTFB)
	}
	if newTotal >= oldTotal {
		t.Errorf("streaming total (%v) should be below store-and-forward (%v)", newTotal, oldTotal)
	}
}

// A HEAD for an uncached blob forwards size from an upstream HEAD and does not
// download or cache the body.
func TestServeBlobHeadDoesNotDownload(t *testing.T) {
	blob := bytes.Repeat([]byte("x"), 1024)
	digest := sha256Digest(blob)
	var sawGet bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			sawGet = true
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
		if r.Method == http.MethodGet {
			_, _ = w.Write(blob)
		}
	}))
	defer upstream.Close()

	p := newTestCache(t, upstream.URL)
	rec := httptest.NewRecorder()
	p.serveBlob(rec, httptest.NewRequest(http.MethodHead, "/v2/img/blobs/"+digest, nil), "reg.example", "img", digest)

	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(len(blob)) {
		t.Errorf("HEAD Content-Length = %q, want %d", rec.Header().Get("Content-Length"), len(blob))
	}
	if sawGet {
		t.Error("HEAD triggered an upstream GET; it must only HEAD")
	}
	if _, err := os.Stat(p.blobPath(digest)); err == nil {
		t.Error("HEAD cached the blob; it must not")
	}
}
