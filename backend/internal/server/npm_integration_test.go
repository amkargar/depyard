package server_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amkargar/depyard/backend/internal/admin"
	"github.com/amkargar/depyard/backend/internal/audit"
	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/cache"
	"github.com/amkargar/depyard/backend/internal/metrics"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/registry/npm"
	"github.com/amkargar/depyard/backend/internal/server"
	"github.com/amkargar/depyard/backend/internal/storage"
	memstore "github.com/amkargar/depyard/backend/internal/store/memory"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

// TestNpmRegistryEndToEnd verifies that npm is wired like any other registry
// type: the shared router handles /r/{slug}, preserves %2F in scoped package
// names, and instance-scoped tokens do not cross registry boundaries.
func TestNpmRegistryEndToEnd(t *testing.T) {
	upstreamTarball := []byte("upstream tarball payload")
	sha1Sum := sha1.Sum(upstreamTarball)
	sha512Sum := sha512.Sum512(upstreamTarball)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/left-pad" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":      "left-pad",
				"dist-tags": map[string]string{"latest": "1.0.0"},
				"versions": map[string]any{
					"1.0.0": map[string]any{
						"name": "left-pad", "version": "1.0.0",
						"dist": map[string]any{
							"tarball":   "http://" + r.Host + "/left-pad/-/left-pad-1.0.0.tgz",
							"shasum":    hex.EncodeToString(sha1Sum[:]),
							"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sha512Sum[:]),
						},
					},
				},
			})
			return
		}
		if r.URL.Path == "/left-pad/-/left-pad-1.0.0.tgz" {
			_, _ = w.Write(upstreamTarball)
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()

	ms := memstore.New()
	fs, err := storage.NewFSBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSBlobStore: %v", err)
	}
	storageSvc := storage.NewService(fs, ms)
	cacheCoord := cache.NewCoordinator(storageSvc)
	authSvc := auth.NewService(ms, time.Hour)
	deps := registry.ServiceDeps{
		Storage:  storageSvc,
		Cache:    cacheCoord,
		Upstream: upstream.NewClient(5 * time.Second),
		Auth:     authSvc,
		Metrics:  metrics.NewCollector(ms),
		Audit:    audit.NewService(ms),
	}
	mgr := registry.NewManager(ms, deps)
	if err := mgr.RegisterType(npm.Registration(npm.NewMemoryStore())); err != nil {
		t.Fatalf("RegisterType: %v", err)
	}
	ctx := context.Background()
	proxy, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name: "npm proxy", Slug: "npm-proxy", Type: npm.RegistryType, Enabled: true,
		Mode: registry.ModeProxy, UpstreamURL: up.URL, Visibility: registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
		CachePolicy: registry.DefaultCachePolicy(),
	})
	if err != nil {
		t.Fatalf("create proxy instance: %v", err)
	}
	hosted, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name: "npm hosted", Slug: "npm-hosted", Type: npm.RegistryType, Enabled: true,
		Mode: registry.ModeHosted, Visibility: registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
		CachePolicy: registry.DefaultCachePolicy(),
	})
	if err != nil {
		t.Fatalf("create hosted instance: %v", err)
	}

	srv := httptest.NewServer(server.New(server.Options{
		Manager:               mgr,
		AdminHandler:          admin.NewHandler(mgr, authSvc, storageSvc, cacheCoord, metrics.NewCollector(ms), audit.NewService(ms), ms, admin.DefaultSettings("", false)),
		EnableRootCompatRoute: func() bool { return false },
		AllowedOrigins:        []string{"*"},
	}))
	defer srv.Close()

	// Proxy route + tarball URL rewriting.
	resp, err := http.Get(srv.URL + "/r/npm-proxy/left-pad")
	if err != nil {
		t.Fatalf("GET packument: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(srv.URL+"/r/npm-proxy/left-pad/-/left-pad-1.0.0.tgz")) {
		t.Fatalf("unexpected packument response %d: %s", resp.StatusCode, body)
	}

	// A token scoped to the hosted registry cannot publish into the proxy one.
	user, err := authSvc.CreateUser(ctx, auth.CreateUserInput{
		Username: "npm-ci", Email: "npm-ci@example.com", Password: "a-long-secret-password", Role: auth.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, scopedToken, err := authSvc.CreateToken(ctx, auth.CreateTokenInput{
		Name: "scoped", UserID: user.ID, RegistryID: hosted.ID, RegistrySlug: hosted.Slug,
		Scopes: []auth.Permission{auth.PermissionPublish, auth.PermissionRead},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	tarball := []byte("locally published tarball")
	sum := sha1.Sum(tarball)
	payload, _ := json.Marshal(map[string]any{
		"name":      "@acme/tools",
		"dist-tags": map[string]string{"latest": "1.0.0"},
		"versions": map[string]any{
			"1.0.0": map[string]any{"name": "@acme/tools", "version": "1.0.0", "dist": map[string]string{"shasum": hex.EncodeToString(sum[:])}},
		},
		"_attachments": map[string]any{
			"@acme/tools-1.0.0.tgz": map[string]any{"data": base64.StdEncoding.EncodeToString(tarball), "length": len(tarball)},
		},
	})
	newRequest := func(target string) *http.Request {
		req, reqErr := http.NewRequest(http.MethodPut, srv.URL+target, bytes.NewReader(payload))
		if reqErr != nil {
			t.Fatalf("new request: %v", reqErr)
		}
		req.Header.Set("Authorization", "Bearer "+scopedToken)
		return req
	}
	resp, err = http.DefaultClient.Do(newRequest("/r/npm-proxy/@acme%2Ftools"))
	if err != nil {
		t.Fatalf("cross-registry publish: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected cross-registry publish to be forbidden, got %d", resp.StatusCode)
	}

	// Scoped publish and packument fetch through the shared router.
	resp, err = http.DefaultClient.Do(newRequest("/r/npm-hosted/@acme%2Ftools"))
	if err != nil {
		t.Fatalf("hosted publish: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 publish, got %d: %s", resp.StatusCode, body)
	}
	resp, err = http.Get(srv.URL + "/r/npm-hosted/@acme%2Ftools")
	if err != nil {
		t.Fatalf("GET scoped packument: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "@acme%2Ftools/-/acme-tools-1.0.0.tgz") {
		t.Fatalf("unexpected scoped packument response %d: %s", resp.StatusCode, body)
	}

	if proxy.Enabled != true || hosted.Enabled != true {
		t.Fatal("instances should remain enabled")
	}
}
