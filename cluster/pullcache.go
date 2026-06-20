package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/philipparndt/go-logger"

	"k3c/config"
)

// The pull-through cache makes image pulls transparent and shared across
// clusters: the generated registries.yaml lists the cache as the first
// mirror endpoint for every configured registry (the real upstream stays
// second, so containerd falls back if the cache is down). containerd
// appends ?ns=<registry> when querying a mirror, so one listener serves
// all upstreams. Blobs are content-addressed and cached forever in one
// global store; manifests by tag are revalidated upstream and served
// stale when the upstream is unreachable.

const pullCacheBlobLimit = 16 << 30 // sanity cap per blob

func pullCacheDir(cfg *config.Config) string {
	return filepath.Join(cfg.BaseDir, "pull-cache")
}

type pullCache struct {
	cfg       *config.Config
	upstreams map[string][]string // registry host -> upstream endpoints
	client    *http.Client
	started   time.Time

	// performance counters (atomic)
	hits, misses        int64
	hitBytes, missBytes int64
	staleServed         int64

	tokenMu sync.Mutex
	tokens  map[string]tokenEntry

	lockMu sync.Mutex
	locks  map[string]*sync.Mutex
}

// PullStats is the pull cache's performance snapshot, served on /stats.
type PullStats struct {
	Hits        int64     `json:"hits"`
	Misses      int64     `json:"misses"`
	HitBytes    int64     `json:"hitBytes"`
	MissBytes   int64     `json:"missBytes"`
	StaleServed int64     `json:"staleServed"`
	Since       time.Time `json:"since"`
}

type tokenEntry struct {
	token   string
	expires time.Time
}

func newPullCache(cfg *config.Config) *pullCache {
	return &pullCache{
		cfg:       cfg,
		upstreams: config.RegistryUpstreams(cfg.Registries),
		client:    &http.Client{Timeout: 30 * time.Minute},
		started:   time.Now(),
		tokens:    map[string]tokenEntry{},
		locks:     map[string]*sync.Mutex{},
	}
}

// countServe records a request served from cache (hit) or via an upstream
// download (miss), with the content size.
func (p *pullCache) countServe(hit bool, path string) {
	size := int64(0)
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	if hit {
		atomic.AddInt64(&p.hits, 1)
		atomic.AddInt64(&p.hitBytes, size)
	} else {
		atomic.AddInt64(&p.misses, 1)
		atomic.AddInt64(&p.missBytes, size)
	}
}

func (p *pullCache) stats() PullStats {
	return PullStats{
		Hits:        atomic.LoadInt64(&p.hits),
		Misses:      atomic.LoadInt64(&p.misses),
		HitBytes:    atomic.LoadInt64(&p.hitBytes),
		MissBytes:   atomic.LoadInt64(&p.missBytes),
		StaleServed: atomic.LoadInt64(&p.staleServed),
		Since:       p.started,
	}
}

// startPullCachePrune prunes the cache in the background: shortly after
// the daemons start and then daily, with the configured retention.
func startPullCachePrune(cfg *config.Config) {
	if cfg.PullCacheRetention <= 0 {
		return
	}
	retention := time.Duration(cfg.PullCacheRetention) * 24 * time.Hour
	go func() {
		time.Sleep(5 * time.Minute)
		for {
			if err := PullCachePrune(cfg, retention); err != nil {
				logger.Warn("pull cache prune: " + err.Error())
			}
			time.Sleep(24 * time.Hour)
		}
	}()
}

// servePullCache runs the registry pull-through cache listener.
func servePullCache(cfg *config.Config) error {
	p := newPullCache(cfg)
	for _, dir := range []string{"blobs", "types", "tags"} {
		if err := os.MkdirAll(filepath.Join(pullCacheDir(cfg), dir), 0o755); err != nil {
			return err
		}
	}
	logger.Info("listening on 0.0.0.0:" + cfg.PullCachePort + " (pull-through cache)")
	ln, err := net.Listen("tcp", "0.0.0.0:"+cfg.PullCachePort)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
	}
	// route the port to the sidecar when it is the active target and publishes it
	port, _ := strconv.Atoi(cfg.PullCachePort)
	return server.Serve(arbitratedListener(ln, cfg, port))
}

var pullPathRe = regexp.MustCompile(`^/v2/(.+)/(manifests|blobs)/([^/]+)$`)

func (p *pullCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v2/" || r.URL.Path == "/v2" {
		w.WriteHeader(http.StatusOK) // the cache itself needs no auth
		return
	}
	if r.URL.Path == "/stats" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.stats())
		return
	}
	m := pullPathRe.FindStringSubmatch(r.URL.Path)
	ns := r.URL.Query().Get("ns")
	if ns == "" {
		// plain registry-mirror clients (e.g. dockerd) send no namespace;
		// they only mirror docker.io
		ns = "docker.io"
	}
	if m == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		// 404 (not 400): clients fall back gracefully on unimplemented
		// registry endpoints, e.g. dockerd querying the referrers API
		http.Error(w, "not supported by the pull cache", http.StatusNotFound)
		return
	}
	name, kind, ref := m[1], m[2], m[3]
	switch {
	case kind == "blobs":
		p.serveBlob(w, r, ns, name, ref)
	case strings.HasPrefix(ref, "sha256:"):
		p.serveManifestByDigest(w, r, ns, name, ref)
	default:
		p.serveManifestByTag(w, r, ns, name, ref)
	}
}

func (p *pullCache) blobPath(digest string) string {
	return filepath.Join(pullCacheDir(p.cfg), "blobs", digest)
}

func (p *pullCache) typePath(digest string) string {
	return filepath.Join(pullCacheDir(p.cfg), "types", digest)
}

func (p *pullCache) tagPath(ns, name, tag string) string {
	return filepath.Join(pullCacheDir(p.cfg), "tags", ns, name, tag)
}

// digestLock serializes concurrent downloads of the same content.
func (p *pullCache) digestLock(digest string) *sync.Mutex {
	p.lockMu.Lock()
	defer p.lockMu.Unlock()
	if l, ok := p.locks[digest]; ok {
		return l
	}
	l := &sync.Mutex{}
	p.locks[digest] = l
	return l
}

func (p *pullCache) serveBlob(w http.ResponseWriter, r *http.Request, ns, name, digest string) {
	if !strings.HasPrefix(digest, "sha256:") {
		http.Error(w, "unsupported digest", http.StatusBadRequest)
		return
	}
	path := p.blobPath(digest)

	// Warm hit: serve straight from the content store.
	if _, err := os.Stat(path); err == nil {
		if r.Method == http.MethodGet {
			p.countServe(true, path)
		}
		p.serveFile(w, r, path, digest, "application/octet-stream")
		return
	}

	// A HEAD miss only needs the size: forward an upstream HEAD without
	// downloading or caching the (potentially huge) blob.
	if r.Method == http.MethodHead {
		p.headUpstreamBlob(w, ns, name, digest)
		return
	}

	// Cold GET miss: serialize same-digest fetches so the blob is downloaded
	// once, then stream it to the node *while* it lands in the cache, instead
	// of fully downloading to the host first (store-and-forward) and only then
	// starting the host->node transfer.
	lock := p.digestLock(digest)
	lock.Lock()
	defer lock.Unlock()
	if _, err := os.Stat(path); err == nil {
		// Another request filled the cache while we waited for the lock.
		p.countServe(true, path)
		p.serveFile(w, r, path, digest, "application/octet-stream")
		return
	}
	p.streamBlob(w, ns, name, digest)
}

// streamBlob fetches a blob from the upstream and tees it simultaneously to the
// node (the HTTP response) and to a temp file in the content store, hashing as
// it flows. The cached copy is committed only if the stream completes and its
// digest matches, so a partial transfer or corrupt upstream never poisons the
// cache. The node may still receive unverified bytes — that is safe because
// containerd verifies the digest itself and retries on a mismatch.
func (p *pullCache) streamBlob(w http.ResponseWriter, ns, name, digest string) {
	resp, err := p.upstreamRequest(http.MethodGet, ns, name, "blobs", digest, "")
	if err != nil {
		logger.Warn("pull cache: blob " + digest + " from " + ns + "/" + name + ": " + err.Error())
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	tmp, err := os.CreateTemp(filepath.Join(pullCacheDir(p.cfg), "blobs"), ".download-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	closed, committed := false, false
	closeTmp := func() {
		if !closed {
			_ = tmp.Close()
			closed = true
		}
	}
	defer func() {
		closeTmp()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	// Headers must be set before the first body byte. Forward the upstream
	// length so the node knows the size up front (registries always send it for
	// blobs); io.Copy from a network reader never takes the sendfile path, so
	// the vmnet corruption that serveFile guards against does not apply here.
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Type", "application/octet-stream")
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}

	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, tmp, hasher), io.LimitReader(resp.Body, pullCacheBlobLimit)); err != nil {
		// The node got a partial body and will retry; leave the cache untouched.
		logger.Warn("pull cache: streaming blob " + digest + ": " + err.Error())
		return
	}
	if got := "sha256:" + hex.EncodeToString(hasher.Sum(nil)); got != digest {
		logger.Warn("pull cache: digest mismatch streaming " + digest + " (got " + got + "), not caching")
		return
	}
	closeTmp()
	if err := os.Rename(tmpName, p.blobPath(digest)); err != nil {
		logger.Warn("pull cache: committing blob " + digest + ": " + err.Error())
		return
	}
	committed = true
	p.countServe(false, p.blobPath(digest))
}

// headUpstreamBlob answers a HEAD for an uncached blob by forwarding an upstream
// HEAD: it reports existence and size without downloading the body.
func (p *pullCache) headUpstreamBlob(w http.ResponseWriter, ns, name, digest string) {
	resp, err := p.upstreamRequest(http.MethodHead, ns, name, "blobs", digest, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Type", "application/octet-stream")
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
}

// serveFile streams a cached file as the response body. It deliberately
// avoids http.ServeFile: ServeFile copies the *os.File straight to the
// socket via the sendfile(2) zero-copy path, which corrupts the response
// body when the peer is across the Apple `container` vmnet virtual NIC —
// the bytes arrive mangled, so the node reads blob content where the HTTP
// status line should be ("malformed HTTP status code <binary>"). Wrapping
// the file in a plain io.Reader defeats the ResponseWriter's sendfile fast
// path, forcing ordinary write(2) syscalls, which the vmnet path handles
// correctly. Range requests are not honored — containerd falls back to a
// full read.
func (p *pullCache) serveFile(w http.ResponseWriter, r *http.Request, path, digest, contentType string) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "content missing", http.StatusBadGateway)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "content missing", http.StatusBadGateway)
		return
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	if r.Method == http.MethodHead {
		return
	}
	// the anonymous wrapper hides the *os.File type, so the http
	// ResponseWriter's ReadFrom cannot take the sendfile path
	_, _ = io.Copy(w, struct{ io.Reader }{f})
}

func (p *pullCache) serveManifestByDigest(w http.ResponseWriter, r *http.Request, ns, name, digest string) {
	hit := true
	if _, err := os.Stat(p.blobPath(digest)); err != nil {
		hit = false
		lock := p.digestLock(digest)
		lock.Lock()
		_, err = os.Stat(p.blobPath(digest))
		if err != nil {
			err = p.fetchContent(ns, name, "manifests", digest, digest, r.Header.Get("Accept"))
		}
		lock.Unlock()
		if err != nil {
			logger.Warn("pull cache: manifest " + digest + " from " + ns + "/" + name + ": " + err.Error())
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	if r.Method == http.MethodGet {
		p.countServe(hit, p.blobPath(digest))
	}
	p.writeManifest(w, r, digest)
}

func (p *pullCache) serveManifestByTag(w http.ResponseWriter, r *http.Request, ns, name, tag string) {
	digest, err := p.resolveTag(ns, name, tag, r.Header.Get("Accept"))
	if err != nil {
		// upstream unreachable: serve the last known digest if we have one
		if cached, readErr := os.ReadFile(p.tagPath(ns, name, tag)); readErr == nil {
			logger.Warn("pull cache: upstream for " + ns + "/" + name + ":" + tag + " unreachable, serving cached manifest")
			atomic.AddInt64(&p.staleServed, 1)
			p.writeManifest(w, r, strings.TrimSpace(string(cached)))
			return
		}
		logger.Warn("pull cache: tag " + ns + "/" + name + ":" + tag + ": " + err.Error())
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	p.writeManifest(w, r, digest)
}

func (p *pullCache) writeManifest(w http.ResponseWriter, r *http.Request, digest string) {
	data, err := os.ReadFile(p.blobPath(digest))
	if err != nil {
		http.Error(w, "manifest content missing", http.StatusBadGateway)
		return
	}
	contentType := "application/vnd.oci.image.manifest.v1+json"
	if t, err := os.ReadFile(p.typePath(digest)); err == nil {
		contentType = strings.TrimSpace(string(t))
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// resolveTag fetches the manifest for a tag from the upstream, caches the
// content by digest, records the tag mapping, and returns the digest.
func (p *pullCache) resolveTag(ns, name, tag, accept string) (string, error) {
	resp, err := p.upstreamRequest(http.MethodGet, ns, name, "manifests", tag, accept)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if hdr := resp.Header.Get("Docker-Content-Digest"); hdr != "" && hdr != digest {
		return "", fmt.Errorf("manifest digest mismatch for %s/%s:%s", ns, name, tag)
	}
	if err := os.WriteFile(p.blobPath(digest), body, 0o644); err != nil {
		return "", err
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		_ = os.WriteFile(p.typePath(digest), []byte(contentType), 0o644)
	}
	if err := os.MkdirAll(filepath.Dir(p.tagPath(ns, name, tag)), 0o755); err != nil {
		return "", err
	}
	_ = os.WriteFile(p.tagPath(ns, name, tag), []byte(digest), 0o644)
	return digest, nil
}

// fetchContent downloads one content item into the cache, verifying its
// digest while streaming.
func (p *pullCache) fetchContent(ns, name, kind, ref, digest, accept string) error {
	resp, err := p.upstreamRequest(http.MethodGet, ns, name, kind, ref, accept)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	tmp, err := os.CreateTemp(filepath.Join(pullCacheDir(p.cfg), "blobs"), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(resp.Body, pullCacheBlobLimit)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := "sha256:" + hex.EncodeToString(hasher.Sum(nil)); got != digest {
		return fmt.Errorf("digest mismatch: want %s, got %s", digest, got)
	}
	if kind == "manifests" {
		if contentType := resp.Header.Get("Content-Type"); contentType != "" {
			_ = os.WriteFile(p.typePath(digest), []byte(contentType), 0o644)
		}
	}
	return os.Rename(tmp.Name(), p.blobPath(digest))
}

// PullCacheList prints the cached images with digest and size.
func PullCacheList(cfg *config.Config) error {
	base := filepath.Join(pullCacheDir(cfg), "tags")
	type row struct{ image, digest, size string }
	var rows []row
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) < 3 {
			return nil
		}
		ns, name, tag := parts[0], strings.Join(parts[1:len(parts)-1], "/"), parts[len(parts)-1]
		digest := ""
		if data, err := os.ReadFile(path); err == nil {
			digest = strings.TrimSpace(string(data))
		}
		size := "-"
		if bytes, complete := cachedImageSize(cfg, digest, 0); bytes > 0 {
			size = fmt.Sprintf("%.1f MB", float64(bytes)/1e6)
			if !complete {
				size += "+"
			}
		}
		rows = append(rows, row{ns + "/" + name + ":" + tag, digest, size})
		return nil
	})
	if len(rows) == 0 {
		fmt.Println("pull cache is empty")
		return nil
	}
	fmt.Printf("%-70s %-18s %s\n", "IMAGE", "DIGEST", "CACHED")
	for _, r := range rows {
		short := strings.TrimPrefix(r.digest, "sha256:")
		if len(short) > 12 {
			short = short[:12]
		}
		fmt.Printf("%-70s %-18s %s\n", r.image, short, r.size)
	}
	return nil
}

// cachedImageSize sums the cached bytes reachable from a manifest digest
// (config + layers, or the children of an index). complete is false when
// referenced content is not in the cache.
func cachedImageSize(cfg *config.Config, digest string, depth int) (bytes int64, complete bool) {
	if digest == "" || depth > 2 {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(pullCacheDir(cfg), "blobs", digest))
	if err != nil {
		return 0, false
	}
	var manifest struct {
		Config struct {
			Size int64 `json:"size"`
		} `json:"config"`
		Layers []struct {
			Size int64 `json:"size"`
		} `json:"layers"`
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return 0, false
	}
	bytes = int64(len(data))
	complete = true
	if len(manifest.Layers) > 0 {
		bytes += manifest.Config.Size
		for _, layer := range manifest.Layers {
			bytes += layer.Size
		}
		return bytes, true
	}
	for _, child := range manifest.Manifests {
		childBytes, childComplete := cachedImageSize(cfg, child.Digest, depth+1)
		bytes += childBytes
		if !childComplete {
			complete = false
		}
	}
	return bytes, complete
}

// PullCacheInfo prints blob count and total cache size.
func PullCacheInfo(cfg *config.Config) error {
	var count int
	var total int64
	_ = filepath.WalkDir(pullCacheDir(cfg), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			count++
			total += info.Size()
		}
		return nil
	})
	fmt.Printf("%s: %d objects, %.1f GB\n", pullCacheDir(cfg), count, float64(total)/1e9)
	return nil
}

// PullCachePrune removes images not pulled within maxAge: tag mappings
// older than the cutoff (their files are rewritten on every revalidation,
// so mtime is the last use) are dropped, content reachable from the
// remaining tags is marked, and unmarked blobs older than the cutoff are
// swept. Recent unmarked blobs stay — digest-pinned pulls have no tag
// mapping anchoring them.
func PullCachePrune(cfg *config.Config, maxAge time.Duration) error {
	if maxAge < 24*time.Hour {
		return fmt.Errorf("retention below one day would sweep the cache wholesale; use `pull-cache clear` for that")
	}
	cutoff := time.Now().Add(-maxAge)
	marked := map[string]bool{}
	prunedTags := 0
	tagsBase := filepath.Join(pullCacheDir(cfg), "tags")
	_ = filepath.WalkDir(tagsBase, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
			prunedTags++
			return nil
		}
		if data, err := os.ReadFile(path); err == nil {
			markCachedTree(cfg, strings.TrimSpace(string(data)), marked, 0)
		}
		return nil
	})

	removed := 0
	var freed int64
	entries, _ := os.ReadDir(filepath.Join(pullCacheDir(cfg), "blobs"))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "sha256:") || marked[name] {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(pullCacheDir(cfg), "blobs", name)); err == nil {
			_ = os.Remove(filepath.Join(pullCacheDir(cfg), "types", name))
			removed++
			freed += info.Size()
		}
	}
	logger.Info(fmt.Sprintf("pruned %d stale tags and %d blobs (%.1f GB freed)",
		prunedTags, removed, float64(freed)/1e9))
	return nil
}

// markCachedTree marks a manifest and everything reachable from it
// (child manifests, config, layers) as in use.
func markCachedTree(cfg *config.Config, digest string, marked map[string]bool, depth int) {
	if digest == "" || marked[digest] || depth > 3 {
		return
	}
	marked[digest] = true
	data, err := os.ReadFile(filepath.Join(pullCacheDir(cfg), "blobs", digest))
	if err != nil {
		return
	}
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return
	}
	if manifest.Config.Digest != "" {
		marked[manifest.Config.Digest] = true
	}
	for _, layer := range manifest.Layers {
		marked[layer.Digest] = true
	}
	for _, child := range manifest.Manifests {
		markCachedTree(cfg, child.Digest, marked, depth+1)
	}
}

// PullCacheStats fetches the running daemons' cache performance counters.
func PullCacheStats(cfg *config.Config) (*PullStats, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + cfg.PullCachePort + "/stats")
	if err != nil {
		return nil, fmt.Errorf("pull cache not reachable (daemons running?): %w", err)
	}
	defer resp.Body.Close()
	var stats PullStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, err
	}
	return &stats, nil
}

// PullCacheStatsPrint prints the cache performance counters.
func PullCacheStatsPrint(cfg *config.Config) error {
	stats, err := PullCacheStats(cfg)
	if err != nil {
		return err
	}
	total := stats.Hits + stats.Misses
	hitPct, bytePct := 0.0, 0.0
	if total > 0 {
		hitPct = float64(stats.Hits) * 100 / float64(total)
	}
	if stats.HitBytes+stats.MissBytes > 0 {
		bytePct = float64(stats.HitBytes) * 100 / float64(stats.HitBytes+stats.MissBytes)
	}
	fmt.Printf("pull cache stats since %s:\n", stats.Since.Format("2006-01-02 15:04"))
	fmt.Printf("  served from cache: %5d requests, %s\n", stats.Hits, humanBytes(stats.HitBytes))
	fmt.Printf("  fetched upstream:  %5d requests, %s\n", stats.Misses, humanBytes(stats.MissBytes))
	fmt.Printf("  hit rate:          %.0f%% of requests, %.0f%% of bytes\n", hitPct, bytePct)
	if stats.StaleServed > 0 {
		fmt.Printf("  stale tags served while offline: %d\n", stats.StaleServed)
	}
	return nil
}

// humanBytes renders a byte count with a sensible unit.
func humanBytes(b int64) string {
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(b)/1e6)
	case b >= 1e3:
		return fmt.Sprintf("%.1f kB", float64(b)/1e3)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// PullCacheClear empties the pull cache (the daemons recreate paths as
// needed on the next download).
func PullCacheClear(cfg *config.Config) error {
	if err := os.RemoveAll(pullCacheDir(cfg)); err != nil {
		return err
	}
	for _, dir := range []string{"blobs", "types", "tags"} {
		if err := os.MkdirAll(filepath.Join(pullCacheDir(cfg), dir), 0o755); err != nil {
			return err
		}
	}
	logger.Info("pull cache cleared")
	return nil
}

// upstreamRequest performs a registry request against the first working
// upstream endpoint for the namespace, handling the bearer token dance.
func (p *pullCache) upstreamRequest(method, ns, name, kind, ref, accept string) (*http.Response, error) {
	endpoints := p.upstreams[ns]
	if len(endpoints) == 0 {
		endpoints = []string{"https://" + ns}
	}
	var lastErr error
	for _, endpoint := range endpoints {
		resp, err := p.endpointRequest(method, endpoint, name, kind, ref, accept)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (p *pullCache) endpointRequest(method, endpoint, name, kind, ref, accept string) (*http.Response, error) {
	reqURL := strings.TrimSuffix(endpoint, "/") + "/v2/" + name + "/" + kind + "/" + ref
	do := func(token string) (*http.Response, error) {
		req, err := http.NewRequest(method, reqURL, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return p.client.Do(req)
	}

	resp, err := do(p.cachedToken(endpoint, name))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("Www-Authenticate")
		_ = resp.Body.Close()
		token, err := p.fetchToken(endpoint, name, challenge)
		if err != nil {
			return nil, err
		}
		if resp, err = do(token); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", reqURL, resp.StatusCode)
	}
	return resp, nil
}

var challengeParamRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

func (p *pullCache) tokenKey(endpoint, name string) string { return endpoint + "|" + name }

func (p *pullCache) cachedToken(endpoint, name string) string {
	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()
	if entry, ok := p.tokens[p.tokenKey(endpoint, name)]; ok && time.Now().Before(entry.expires) {
		return entry.token
	}
	return ""
}

// fetchToken implements the anonymous bearer token flow of the registry
// HTTP API ("Www-Authenticate: Bearer realm=..,service=..,scope=..").
func (p *pullCache) fetchToken(endpoint, name, challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("unsupported auth challenge %q", challenge)
	}
	params := map[string]string{}
	for _, m := range challengeParamRe.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("auth challenge without realm: %q", challenge)
	}
	query := url.Values{}
	if params["service"] != "" {
		query.Set("service", params["service"])
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + name + ":pull"
	}
	query.Set("scope", scope)

	resp, err := p.client.Get(realm + "?" + query.Encode())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("token response: %w", err)
	}
	token := payload.Token
	if token == "" {
		token = payload.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("token endpoint %s returned no token", realm)
	}
	ttl := payload.ExpiresIn
	if ttl < 60 {
		ttl = 60
	}
	p.tokenMu.Lock()
	p.tokens[p.tokenKey(endpoint, name)] = tokenEntry{token: token, expires: time.Now().Add(time.Duration(ttl-30) * time.Second)}
	p.tokenMu.Unlock()
	return token, nil
}
