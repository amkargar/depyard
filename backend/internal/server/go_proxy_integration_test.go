package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/amkargar/depyard/backend/internal/admin"
	"github.com/amkargar/depyard/backend/internal/audit"
	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/cache"
	"github.com/amkargar/depyard/backend/internal/metrics"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/registry/goproxy"
	"github.com/amkargar/depyard/backend/internal/server"
	"github.com/amkargar/depyard/backend/internal/storage"
	memstore "github.com/amkargar/depyard/backend/internal/store/memory"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

// goUpstream is a fake GOPROXY upstream that serves a fixed module.
type goUpstream struct {
	srv *httptest.Server
}

func newGoUpstream(t *testing.T) *goUpstream {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /github.com/pkg/errors/@v/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "v0.8.0\nv0.9.1\n")
	})
	mux.HandleFunc("GET /github.com/pkg/errors/@v/v0.9.1.info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Version": "v0.9.1",
			"Time":    "2022-01-02T03:04:05Z",
		})
	})
	mux.HandleFunc("GET /github.com/pkg/errors/@v/v0.9.1.mod", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "module github.com/pkg/errors\n\ngo 1.17\n")
	})
	mux.HandleFunc("GET /github.com/pkg/errors/@v/v0.9.1.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = io.WriteString(w, "PK\x03\x04fake-zip-content-for-github.com/pkg/errors@v0.9.1")
	})

	// A module that does not exist upstream.
	mux.HandleFunc("GET /github.com/nope/missing/@v/list", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "not found")
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &goUpstream{srv: srv}
}

// newGoProxyHarness spins up a full server with a single Go module proxy registry.
func newGoProxyHarness(t *testing.T, upstreamURL string) (*httptest.Server, string) {
	t.Helper()

	ms := memstore.New()
	fsStore, err := storage.NewFSBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSBlobStore: %v", err)
	}
	storageSvc := storage.NewService(fsStore, ms)
	cacheCoord := cache.NewCoordinator(storageSvc)
	upstreamClient := upstream.NewClient(10 * 1000000000)
	authSvc := auth.NewService(ms, 3600)
	metricsCol := metrics.NewCollector(ms)
	auditSvc := audit.NewService(ms)

	deps := registry.ServiceDeps{
		Storage:  storageSvc,
		Cache:    cacheCoord,
		Upstream: upstreamClient,
		Auth:     authSvc,
		Metrics:  metricsCol,
		Audit:    auditSvc,
	}
	mgr := registry.NewManager(ms, deps)
	if err := mgr.RegisterType(goproxy.Registration(goproxy.NewMemoryStore())); err != nil {
		t.Fatalf("RegisterType: %v", err)
	}

	ctx := context.Background()
	inst, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name:        "Go Proxy",
		Slug:        "go-proxy",
		Type:        goproxy.RegistryType,
		Enabled:     true,
		Mode:        registry.ModeProxy,
		UpstreamURL: upstreamURL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy: registry.AuthPolicy{
			AllowAnonymousRead: true,
			RequireAuthPublish: true,
		},
		CachePolicy: registry.DefaultCachePolicy(),
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	settings := admin.DefaultSettings("", false)
	adminHandler := admin.NewHandler(mgr, authSvc, storageSvc, cacheCoord, metricsCol, auditSvc, ms, settings)

	srv := httptest.NewServer(server.New(server.Options{
		Manager:               mgr,
		AdminHandler:          adminHandler,
		PublicBaseURLFunc:     func() string { return "" },
		EnableRootCompatRoute: func() bool { return false },
		AllowedOrigins:        []string{"*"},
	}))
	t.Cleanup(srv.Close)

	return srv, inst.ID
}

func TestGoProxyServesModuleMetadataAndArchives(t *testing.T) {
	up := newGoUpstream(t)
	srv, _ := newGoProxyHarness(t, up.srv.URL)

	client := &http.Client{}

	// 1. Version list.
	resp, err := client.Get(srv.URL + "/r/go-proxy/github.com/pkg/errors/@v/list")
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on list, got %d (%s)", resp.StatusCode, string(body))
	}
	if got := strings.TrimSpace(string(body)); got != "v0.8.0\nv0.9.1" {
		t.Fatalf("unexpected version list: %q", got)
	}

	// 2. Version info.
	resp, err = client.Get(srv.URL + "/r/go-proxy/github.com/pkg/errors/@v/v0.9.1.info")
	if err != nil {
		t.Fatalf("GET info: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on info, got %d (%s)", resp.StatusCode, string(body))
	}
	var info goproxy.VersionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if info.Version != "v0.9.1" {
		t.Fatalf("unexpected info version: %+v", info)
	}
	if info.Time.IsZero() {
		t.Fatalf("expected a non-zero info time, got %+v", info)
	}

	// 3. go.mod content.
	resp, err = client.Get(srv.URL + "/r/go-proxy/github.com/pkg/errors/@v/v0.9.1.mod")
	if err != nil {
		t.Fatalf("GET mod: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on mod, got %d (%s)", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "module github.com/pkg/errors") {
		t.Fatalf("unexpected go.mod body: %q", string(body))
	}

	// 4. Module zip (streamed and cached).
	resp, err = client.Get(srv.URL + "/r/go-proxy/github.com/pkg/errors/@v/v0.9.1.zip")
	if err != nil {
		t.Fatalf("GET zip: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on zip, got %d (%s)", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "fake-zip-content") {
		t.Fatalf("unexpected zip body: %q", string(body))
	}
	if resp.Header.Get("X-Registry-Cache") != "MISS" {
		t.Fatalf("expected a cache MISS on the first zip download, got %q", resp.Header.Get("X-Registry-Cache"))
	}

	// 5. The second zip download is served from the local cache.
	resp, err = client.Get(srv.URL + "/r/go-proxy/github.com/pkg/errors/@v/v0.9.1.zip")
	if err != nil {
		t.Fatalf("GET zip (2nd): %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("X-Registry-Cache") != "HIT" {
		t.Fatalf("expected a cache HIT on the second zip download, got %q", resp.Header.Get("X-Registry-Cache"))
	}
}

func TestGoProxyProtocolErrors(t *testing.T) {
	up := newGoUpstream(t)
	srv, _ := newGoProxyHarness(t, up.srv.URL)
	client := &http.Client{}

	// A path that is not one of the four protocol endpoints must 404, never list a directory.
	resp, err := client.Get(srv.URL + "/r/go-proxy/github.com/pkg/errors/@v/v0.9.1.txt")
	if err != nil {
		t.Fatalf("GET unsupported file: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on an unsupported file, got %d", resp.StatusCode)
	}

	// A missing upstream module must 404.
	resp, err = client.Get(srv.URL + "/r/go-proxy/github.com/nope/missing/@v/list")
	if err != nil {
		t.Fatalf("GET missing list: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on a missing module, got %d", resp.StatusCode)
	}

	// An invalid module path must 404.
	resp, err = client.Get(srv.URL + "/r/go-proxy/GitHub.com/Pkg/Errors/@v/list")
	if err != nil {
		t.Fatalf("GET invalid module: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 on an invalid module path, got %d", resp.StatusCode)
	}
}

func TestGoProxyTypeIsRegistered(t *testing.T) {
	up := newGoUpstream(t)
	srv, _ := newGoProxyHarness(t, up.srv.URL)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "goproxy") {
		t.Fatalf("expected the goproxy type to be listed in /healthz, got %s", string(body))
	}
}

func TestGoProxyAdminTypeMetadata(t *testing.T) {
	up := newGoUpstream(t)
	srv, _ := newGoProxyHarness(t, up.srv.URL)

	// The admin API must expose the new type with its capabilities.
	resp, err := http.Get(srv.URL + "/api/public/registries")
	if err != nil {
		t.Fatalf("GET public registries: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "goproxy") {
		t.Fatalf("expected the goproxy registry to be listed, got %s", string(body))
	}
	fmt.Fprintln(os.Stderr, "public registries:", string(body))
}
