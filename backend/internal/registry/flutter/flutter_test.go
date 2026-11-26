package flutter_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amkargar/depyard/backend/internal/audit"
	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/cache"
	"github.com/amkargar/depyard/backend/internal/metrics"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/registry/flutter"
	"github.com/amkargar/depyard/backend/internal/storage"
	memstore "github.com/amkargar/depyard/backend/internal/store/memory"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

func TestParseConfigValidation(t *testing.T) {
	cfg, err := flutter.ParseConfig(nil)
	if err != nil {
		t.Fatalf("default config failed: %v", err)
	}
	if len(cfg.AllowedPrefixes) != len(flutter.DefaultAllowedPrefixes) || !cfg.RewriteManifestURLs || !cfg.ServeStaleOnError {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	if _, err := flutter.ParseConfig(map[string]any{"unknown_field": true}); err == nil {
		t.Fatal("expected unknown config field to be rejected")
	}
	if _, err := flutter.ParseConfig(map[string]any{"allowed_prefixes": []string{"../etc"}}); err == nil {
		t.Fatal("expected traversal prefix to be rejected")
	}
	if _, err := flutter.ParseConfig(map[string]any{"manifest_ttl_seconds": -5}); err == nil {
		t.Fatal("expected negative manifest TTL to be rejected")
	}
}

func TestFlutterStorageMirrorManifestRewriteCacheAndStaleFallback(t *testing.T) {
	var manifestHits atomic.Int32
	var binaryHits atomic.Int32
	var upstreamDown atomic.Bool
	var seenAuthHeader atomic.Value

	var mockUpstream *httptest.Server
	mockUpstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuthHeader.Store(r.Header.Get("Authorization"))
		if upstreamDown.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/flutter_infra_release/releases/releases_linux.json":
			manifestHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"base_url": mockUpstream.URL + "/flutter_infra_release/releases",
				"releases": []map[string]any{
					{"version": "3.27.1", "archive": "stable/linux/flutter_linux_3.27.1-stable.tar.xz"},
				},
			})
		case "/flutter_infra_release/releases/stable/linux/flutter_linux_3.27.1-stable.tar.xz":
			binaryHits.Add(1)
			w.Header().Set("Content-Type", "application/x-xz")
			_, _ = w.Write([]byte("fake-flutter-sdk-tar-xz-payload-0123456789"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockUpstream.Close()

	ms := memstore.New()
	fs, err := storage.NewFSBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("fs store: %v", err)
	}
	storageSvc := storage.NewService(fs, ms)
	deps := registry.ServiceDeps{
		Storage:  storageSvc,
		Cache:    cache.NewCoordinator(storageSvc),
		Upstream: upstream.NewClient(5 * time.Second),
		Auth:     auth.NewService(ms, time.Hour),
		Metrics:  metrics.NewCollector(ms),
		Audit:    audit.NewService(ms),
	}

	inst := &registry.Instance{
		ID:          "fl-1",
		Name:        "Flutter Storage",
		Slug:        "flutter-storage",
		Type:        flutter.RegistryType,
		Enabled:     true,
		Mode:        registry.ModeProxy,
		UpstreamURL: mockUpstream.URL,
		UpstreamConfig: &registry.UpstreamConfig{
			AuthType:  "bearer",
			AuthToken: "upstream-secret-token",
		},
		Visibility: registry.VisibilityPublic,
		AuthPolicy: registry.AuthPolicy{AllowAnonymousRead: true},
		Config: map[string]any{
			"manifest_ttl_seconds":  1,
			"rewrite_manifest_urls": true,
			"serve_stale_on_error":  true,
		},
	}

	flReg, err := flutter.New(inst, deps, flutter.NewMemoryStore())
	if err != nil {
		t.Fatalf("flutter.New: %v", err)
	}

	doReq := func(method, target, rangeHdr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		req = req.WithContext(registry.WithBaseURL(req.Context(), "https://mirror.example.com/r/flutter-storage"))
		if rangeHdr != "" {
			req.Header.Set("Range", rangeHdr)
		}
		rec := httptest.NewRecorder()
		flReg.Handler().ServeHTTP(rec, req)
		return rec
	}

	// 1. Disallowed prefix & path traversal rejected
	if rec := doReq(http.MethodGet, "/secret-bucket/keys.json", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed prefix, got %d", rec.Code)
	}
	if rec := doReq(http.MethodGet, "/flutter_infra_release/../../etc/passwd", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for traversal, got %d", rec.Code)
	}

	// 2. Manifest fetch rewrites base_url & forwards per-registry upstream auth header
	rec1 := doReq(http.MethodGet, "/flutter_infra_release/releases/releases_linux.json", "")
	if rec1.Code != http.StatusOK || rec1.Header().Get("X-Registry-Cache") != "MISS" {
		t.Fatalf("manifest miss expected 200 MISS, got %d %q", rec1.Code, rec1.Header().Get("X-Registry-Cache"))
	}
	var manifest map[string]any
	if err := json.Unmarshal(rec1.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	wantBase := "https://mirror.example.com/r/flutter-storage/flutter_infra_release/releases"
	if manifest["base_url"] != wantBase {
		t.Fatalf("expected rewritten base_url %q, got %v", wantBase, manifest["base_url"])
	}
	if gotAuth, _ := seenAuthHeader.Load().(string); gotAuth != "Bearer upstream-secret-token" {
		t.Fatalf("expected upstream bearer token forwarded, got %q", gotAuth)
	}

	// 3. Immediate second request is cache HIT (upstream not called again)
	rec2 := doReq(http.MethodGet, "/flutter_infra_release/releases/releases_linux.json", "")
	if rec2.Code != http.StatusOK || rec2.Header().Get("X-Registry-Cache") != "HIT" {
		t.Fatalf("expected manifest HIT, got %d %q", rec2.Code, rec2.Header().Get("X-Registry-Cache"))
	}
	if manifestHits.Load() != 1 {
		t.Fatalf("expected 1 upstream manifest call, got %d", manifestHits.Load())
	}

	// 4. After TTL expires + upstream goes down, stale-while-error returns cached manifest with STALE header
	time.Sleep(1100 * time.Millisecond)
	upstreamDown.Store(true)
	rec3 := doReq(http.MethodGet, "/flutter_infra_release/releases/releases_linux.json", "")
	if rec3.Code != http.StatusOK || rec3.Header().Get("X-Registry-Cache") != "STALE" {
		t.Fatalf("expected stale fallback 200 STALE, got %d %q", rec3.Code, rec3.Header().Get("X-Registry-Cache"))
	}
	upstreamDown.Store(false)

	// 5. Binary artifact GET + HEAD + Range + Cache HIT
	binPath := "/flutter_infra_release/releases/stable/linux/flutter_linux_3.27.1-stable.tar.xz"
	b1 := doReq(http.MethodGet, binPath, "")
	if b1.Code != http.StatusOK || b1.Header().Get("X-Registry-Cache") != "MISS" || b1.Header().Get("X-Checksum-SHA256") == "" {
		t.Fatalf("binary miss failed: status=%d cache=%q", b1.Code, b1.Header().Get("X-Registry-Cache"))
	}
	b2 := doReq(http.MethodGet, binPath, "bytes=0-3")
	if b2.Code != http.StatusPartialContent || b2.Body.String() != "fake" || b2.Header().Get("X-Registry-Cache") != "HIT" {
		t.Fatalf("range request failed: status=%d body=%q cache=%q", b2.Code, b2.Body.String(), b2.Header().Get("X-Registry-Cache"))
	}
	bHead := doReq(http.MethodHead, binPath, "")
	if bHead.Code != http.StatusOK || bHead.Body.Len() != 0 {
		t.Fatalf("HEAD failed: status=%d bodyLen=%d", bHead.Code, bHead.Body.Len())
	}
	if binaryHits.Load() != 1 {
		t.Fatalf("expected 1 upstream binary download, got %d", binaryHits.Load())
	}

	// 6. Explorer JSON API + CompatPathMatcher + PurgeCache
	apiRec := doReq(http.MethodGet, "/api/artifacts", "")
	if apiRec.Code != http.StatusOK || !strings.Contains(apiRec.Body.String(), "flutter_linux_3.27.1-stable.tar.xz") {
		body, _ := io.ReadAll(apiRec.Body)
		t.Fatalf("explorer API failed: %d %s", apiRec.Code, string(body))
	}
	if !flReg.MatchesCompatPath("/flutter_infra_release/releases/releases_linux.json") || !flReg.MatchesCompatPath("/download.flutter.io/io/flutter/a.pom") {
		t.Fatal("expected CompatPathMatcher to match Flutter storage prefixes")
	}
	if flReg.MatchesCompatPath("/api/packages/http") {
		t.Fatal("expected CompatPathMatcher NOT to match Pub paths")
	}
	purgeRes, err := flReg.PurgeCache(context.Background())
	if err != nil || purgeRes.RemovedRefs < 2 {
		t.Fatalf("expected purge to remove >=2 refs, got %+v err=%v", purgeRes, err)
	}
}
