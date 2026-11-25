package server_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/admin"
	"github.com/amkargar/mirror-registry/backend/internal/audit"
	"github.com/amkargar/mirror-registry/backend/internal/auth"
	"github.com/amkargar/mirror-registry/backend/internal/cache"
	"github.com/amkargar/mirror-registry/backend/internal/metrics"
	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/registry/pub"
	"github.com/amkargar/mirror-registry/backend/internal/server"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
	memstore "github.com/amkargar/mirror-registry/backend/internal/store/memory"
	"github.com/amkargar/mirror-registry/backend/internal/upstream"
)

// mockBinRegistry is a minimal TEST-ONLY registry type (never shipped in production).
// It exercises: independent registration, raw path encoding preservation, isolated routing,
// unsupported capabilities (no Publish, no ArtifactDeletion, no CacheWarm), and shared storage.
type mockBinRegistry struct {
	inst *registry.Instance
	deps registry.ServiceDeps
}

func (m *mockBinRegistry) Type() string { return "mockbin" }
func (m *mockBinRegistry) Capabilities() registry.Capabilities {
	return registry.Capabilities{
		Proxy:            true,
		Hosted:           false,
		Publish:          false,
		ArtifactDeletion: false,
		CacheWarm:        false,
	}
}
func (m *mockBinRegistry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := m.deps.Auth.AuthenticateRequest(r.Context(), r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := m.deps.Auth.Authorize(principal, auth.AuthRequest{
			Policy:     m.inst.AccessPolicy(),
			Permission: auth.PermissionRead,
		}); err != nil {
			if err == auth.ErrUnauthenticated {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			} else {
				http.Error(w, "forbidden", http.StatusForbidden)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"slug":     m.inst.Slug,
			"path":     r.URL.Path,
			"raw_path": r.URL.RawPath,
			"base_url": registry.BaseURLFromContext(r.Context()),
		})
	})
}

func mockBinRegistration() registry.Registration {
	return registry.Registration{
		Type:         "mockbin",
		DisplayName:  "Mock Binary (Test Only)",
		Description:  "Test-only registry verifying extensibility boundaries.",
		Capabilities: (&mockBinRegistry{}).Capabilities(),
		ValidateConfig: func(cfg map[string]any) error {
			if tag, ok := cfg["release_tag"]; ok {
				if s, ok := tag.(string); !ok || s == "" {
					return fmt.Errorf("release_tag must be a non-empty string")
				}
			}
			return nil
		},
		Create: func(_ context.Context, inst *registry.Instance, deps registry.ServiceDeps) (registry.Registry, error) {
			return &mockBinRegistry{inst: inst, deps: deps}, nil
		},
	}
}

func makePubTarball(t *testing.T, pkgName, version, bodyExtra string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	pubspec := fmt.Sprintf("name: %s\nversion: %s\ndescription: Test package %s\n", pkgName, version, pkgName)
	files := map[string]string{
		"pubspec.yaml":       pubspec,
		"README.md":          "# " + pkgName + "\n" + bodyExtra,
		"lib/" + pkgName + ".dart": "const v = '" + version + "'; " + bodyExtra,
	}
	for name, content := range files {
		data := []byte(content)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gzw.Close()
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

func publishViaDartFlow(t *testing.T, client *http.Client, baseURL, token string, archive []byte) (int, string) {
	t.Helper()
	// Step 1: GET /api/packages/versions/new
	req1, _ := http.NewRequest(http.MethodGet, baseURL+"/api/packages/versions/new", nil)
	if token != "" {
		req1.Header.Set("Authorization", "Bearer "+token)
	}
	req1.Header.Set("Accept", pub.PubV2MediaType)
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("step1 request: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp1.Body)
		return resp1.StatusCode, string(b)
	}
	var uploadInfo pub.UploadInfoResponse
	if err := json.NewDecoder(resp1.Body).Decode(&uploadInfo); err != nil {
		t.Fatalf("decode upload info: %v", err)
	}

	// Step 2: POST multipart file to returned upload URL
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	part, _ := mw.CreateFormFile("file", "package.tar.gz")
	_, _ = part.Write(archive)
	_ = mw.Close()

	req2, _ := http.NewRequest(http.MethodPost, uploadInfo.URL, &mp)
	req2.Header.Set("Content-Type", mw.FormDataContentType())
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("step2 upload: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp2.Body)
		return resp2.StatusCode, string(b)
	}
	finishURL := resp2.Header.Get("Location")
	if finishURL == "" {
		t.Fatalf("expected Location header on 204 upload response")
	}

	// Step 3: GET finish URL
	req3, _ := http.NewRequest(http.MethodGet, finishURL, nil)
	req3.Header.Set("Accept", pub.PubV2MediaType)
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("step3 finish: %v", err)
	}
	defer resp3.Body.Close()
	b, _ := io.ReadAll(resp3.Body)
	return resp3.StatusCode, string(b)
}

func TestMultiRegistryExtensibilityAndDartPubCompatibility(t *testing.T) {
	ctx := context.Background()

	// Mock upstream pub.dev server
	var upstreamArchiveHits atomic.Int32
	flutterTar, flutterSHA := makePubTarball(t, "flutter_bloc", "8.1.6", "upstream-payload")
	sharedTar, sharedSHA := makePubTarball(t, "shared_pkg", "1.0.0", "identical-binary-content")
	acmeUpstreamTar, acmeUpstreamSHA := makePubTarball(t, "acme_core", "9.9.9", "upstream-spoof-attempt")

	var upstreamSrv *httptest.Server
	upstreamSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/packages/flutter_bloc":
			w.Header().Set("Content-Type", pub.PubV2MediaType)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "flutter_bloc",
				"versions": []map[string]any{{
					"version":        "8.1.6",
					"archive_url":    upstreamSrv.URL + "/packages/flutter_bloc/versions/8.1.6.tar.gz",
					"archive_sha256": flutterSHA,
					"pubspec":        map[string]any{"name": "flutter_bloc", "version": "8.1.6", "description": "Bloc state management"},
					"published":      "2025-01-10T00:00:00Z",
				}},
			})
		case "/packages/flutter_bloc/versions/8.1.6.tar.gz":
			upstreamArchiveHits.Add(1)
			time.Sleep(40 * time.Millisecond) // Encourage concurrent requests to coalesce via singleflight
			_, _ = w.Write(flutterTar)
		case "/api/packages/shared_pkg":
			w.Header().Set("Content-Type", pub.PubV2MediaType)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "shared_pkg",
				"versions": []map[string]any{{
					"version":        "1.0.0",
					"archive_url":    upstreamSrv.URL + "/packages/shared_pkg/versions/1.0.0.tar.gz",
					"archive_sha256": sharedSHA,
					"pubspec":        map[string]any{"name": "shared_pkg", "version": "1.0.0"},
				}},
			})
		case "/packages/shared_pkg/versions/1.0.0.tar.gz":
			_, _ = w.Write(sharedTar)
		case "/api/packages/acme_core":
			w.Header().Set("Content-Type", pub.PubV2MediaType)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "acme_core",
				"versions": []map[string]any{{
					"version":        "9.9.9",
					"archive_url":    upstreamSrv.URL + "/packages/acme_core/versions/9.9.9.tar.gz",
					"archive_sha256": acmeUpstreamSHA,
					"pubspec":        map[string]any{"name": "acme_core", "version": "9.9.9"},
				}},
			})
		case "/packages/acme_core/versions/9.9.9.tar.gz":
			_, _ = w.Write(acmeUpstreamTar)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamSrv.Close()

	// Wire entire platform with shared core + Pub + test-only mockbin
	ms := memstore.New()
	fsStore, err := storage.NewFSBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSBlobStore: %v", err)
	}
	storageSvc := storage.NewService(fsStore, ms)
	cacheCoord := cache.NewCoordinator(storageSvc)
	upClient := upstream.NewClient(10 * time.Second)
	authSvc := auth.NewService(ms, time.Hour)
	metricsCol := metrics.NewCollector(ms)
	auditSvc := audit.NewService(ms)
	pubStore := pub.NewMemoryStore()

	_, _ = authSvc.EnsureBootstrapAdmin(ctx, "admin", "admin@test.local", "adminpass")
	adminUser, adminSessionToken, _, err := authSvc.Login(ctx, "admin", "adminpass", "test", "127.0.0.1")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}

	mgr := registry.NewManager(ms, registry.ServiceDeps{
		Storage:  storageSvc,
		Cache:    cacheCoord,
		Upstream: upClient,
		Auth:     authSvc,
		Metrics:  metricsCol,
		Audit:    auditSvc,
	})
	if err := mgr.RegisterType(pub.Registration(pubStore)); err != nil {
		t.Fatalf("RegisterType pub: %v", err)
	}
	if err := mgr.RegisterType(mockBinRegistration()); err != nil {
		t.Fatalf("RegisterType mockbin: %v", err)
	}

	// Create multiple coexisting instances:
	// 1. pub-public (default, hybrid, public)
	pubPublic, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name:        "Pub Public",
		Slug:        "pub-public",
		Type:        "pub",
		Enabled:     true,
		Mode:        registry.ModeHybrid,
		UpstreamURL: upstreamSrv.URL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true, RequireAuthPublish: true},
		CachePolicy: registry.DefaultCachePolicy(),
		IsDefault:   true,
	})
	if err != nil {
		t.Fatalf("create pub-public: %v", err)
	}

	// 2. pub-private (hosted, private)
	pubPrivate, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name:       "Pub Private",
		Slug:       "pub-private",
		Type:       "pub",
		Enabled:    true,
		Mode:       registry.ModeHosted,
		Visibility: registry.VisibilityPrivate,
		AuthPolicy: registry.AuthPolicy{AllowAnonymousRead: false, RequireAuthPublish: true},
	})
	if err != nil {
		t.Fatalf("create pub-private: %v", err)
	}

	// 3. pub-team-a (hosted, private, tiny 200-byte quota to test quota isolation!)
	pubTeamA, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name:              "Pub Team A",
		Slug:              "pub-team-a",
		Type:              "pub",
		Enabled:           true,
		Mode:              registry.ModeHosted,
		Visibility:        registry.VisibilityPrivate,
		StorageQuotaBytes: 50, // Deliberately smaller than any tar.gz
		AuthPolicy:        registry.AuthPolicy{AllowAnonymousRead: false, RequireAuthPublish: true},
	})
	if err != nil {
		t.Fatalf("create pub-team-a: %v", err)
	}

	// 4. mock-bin (test-only registry type, proxy-only, no deletion/warm)
	mockBin, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name:        "Mock Binary Proxy",
		Slug:        "mock-bin",
		Type:        "mockbin",
		Enabled:     true,
		Mode:        registry.ModeProxy,
		UpstreamURL: upstreamSrv.URL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
		Config:      map[string]any{"release_tag": "v1"},
	})
	if err != nil {
		t.Fatalf("create mock-bin: %v", err)
	}

	settings := admin.DefaultSettings("", true)
	adminHandler := admin.NewHandler(mgr, authSvc, storageSvc, cacheCoord, metricsCol, auditSvc, ms, settings)
	srv := server.New(server.Options{
		Manager:               mgr,
		AdminHandler:          adminHandler,
		EnableRootCompatRoute: func() bool { return true },
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	httpClient := ts.Client()

	// === VERIFICATION 1: Isolated routing & raw path encoding preservation on test-only registry ===
	t.Run("IsolatedRoutingAndRawPathPreservation", func(t *testing.T) {
		resp, err := httpClient.Get(ts.URL + "/r/mock-bin/org%2Fpkg/v1.0")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 on mock-bin, got %d", resp.StatusCode)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if out["slug"] != "mock-bin" {
			t.Fatalf("unexpected slug: %+v", out)
		}
		if out["raw_path"] != "/org%2Fpkg/v1.0" {
			t.Fatalf("expected percent-encoded raw_path preserved, got %q", out["raw_path"])
		}
	})

	// === VERIFICATION 2: Unsupported capabilities are NOT exposed via Admin API ===
	t.Run("UnsupportedCapabilitiesNotExposed", func(t *testing.T) {
		// mock-bin declares ArtifactDeletion: false and CacheWarm: false -> admin API must return 501
		reqDel, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/admin/registries/"+mockBin.ID+"/artifacts/foo", nil)
		reqDel.Header.Set("Authorization", "Bearer "+adminSessionToken)
		respDel, err := httpClient.Do(reqDel)
		if err != nil {
			t.Fatal(err)
		}
		_ = respDel.Body.Close()
		if respDel.StatusCode != http.StatusNotImplemented {
			t.Fatalf("expected 501 Not Implemented for unsupported ArtifactDeletion, got %d", respDel.StatusCode)
		}

		reqWarm, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/registries/"+mockBin.ID+"/cache/warm", strings.NewReader(`{"name":"foo"}`))
		reqWarm.Header.Set("Authorization", "Bearer "+adminSessionToken)
		respWarm, err := httpClient.Do(reqWarm)
		if err != nil {
			t.Fatal(err)
		}
		_ = respWarm.Body.Close()
		if respWarm.StatusCode != http.StatusNotImplemented {
			t.Fatalf("expected 501 Not Implemented for unsupported CacheWarm, got %d", respWarm.StatusCode)
		}
	})

	// === VERIFICATION 3: Dart Pub v2 Upstream Mirroring, archive_url Rewriting, Singleflight Dedup, Root Compat ===
	t.Run("DartPubMirrorSingleflightAndRootCompat", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/r/pub-public/api/packages/flutter_bloc", nil)
		req.Header.Set("Accept", pub.PubV2MediaType)
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), pub.PubV2MediaType) {
			t.Fatalf("expected pub v2 content-type, got %s", resp.Header.Get("Content-Type"))
		}
		var pkgResp pub.PackageResponse
		if err := json.NewDecoder(resp.Body).Decode(&pkgResp); err != nil {
			t.Fatal(err)
		}
		wantArchiveURL := ts.URL + "/r/pub-public/packages/flutter_bloc/versions/8.1.6.tar.gz"
		if pkgResp.Latest == nil || pkgResp.Latest.ArchiveURL != wantArchiveURL {
			t.Fatalf("expected rewritten archive_url %q, got %+v", wantArchiveURL, pkgResp.Latest)
		}

		// 6 concurrent downloads must coalesce into 1 upstream fetch via singleflight!
		var wg sync.WaitGroup
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, err := httpClient.Get(wantArchiveURL)
				if err != nil {
					t.Errorf("concurrent download failed: %v", err)
					return
				}
				data, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()
				if r.StatusCode != http.StatusOK || !bytes.Equal(data, flutterTar) {
					t.Errorf("unexpected archive response status=%d len=%d", r.StatusCode, len(data))
				}
			}()
		}
		wg.Wait()
		if hits := upstreamArchiveHits.Load(); hits != 1 {
			t.Fatalf("expected singleflight to coalesce 6 concurrent downloads into 1 upstream fetch, got %d", hits)
		}

		// Root compatibility route must serve the default pub-public registry with root archive_url!
		compatResp, err := httpClient.Get(ts.URL + "/api/packages/flutter_bloc")
		if err != nil {
			t.Fatal(err)
		}
		defer compatResp.Body.Close()
		if compatResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 on root compat route, got %d", compatResp.StatusCode)
		}
		var compatPkg pub.PackageResponse
		_ = json.NewDecoder(compatResp.Body).Decode(&compatPkg)
		wantRootArchiveURL := ts.URL + "/packages/flutter_bloc/versions/8.1.6.tar.gz"
		if compatPkg.Latest == nil || compatPkg.Latest.ArchiveURL != wantRootArchiveURL {
			t.Fatalf("expected root compat archive_url %q, got %+v", wantRootArchiveURL, compatPkg.Latest)
		}
	})

	// === VERIFICATION 4: Dart Pub Publish Flow, Permission Isolation, Namespace Restrictions, Hybrid Protection ===
	t.Run("PublishPermissionIsolationAndHybridProtection", func(t *testing.T) {
		// Create token scoped ONLY to pub-private (read + publish, namespaces: ["shared_*", "priv_*"])
		_, privToken, err := authSvc.CreateToken(ctx, auth.CreateTokenInput{
			Name:         "priv-ci",
			UserID:       adminUser.ID,
			RegistryID:   pubPrivate.ID,
			RegistrySlug: pubPrivate.Slug,
			RegistryType: "pub",
			Scopes:       []auth.Permission{auth.PermissionRead, auth.PermissionPublish},
			Namespaces:   []string{"shared_*", "priv_*"},
		})
		if err != nil {
			t.Fatal(err)
		}

		// Publishing to pub-private with valid token & matching namespace MUST succeed
		status, body := publishViaDartFlow(t, httpClient, ts.URL+"/r/pub-private", privToken, sharedTar)
		if status != http.StatusOK || !strings.Contains(body, "Successfully uploaded") {
			t.Fatalf("expected 200 publish success, got %d: %s", status, body)
		}

		// Duplicate version MUST be rejected (immutability!)
		dupStatus, _ := publishViaDartFlow(t, httpClient, ts.URL+"/r/pub-private", privToken, sharedTar)
		if dupStatus != http.StatusConflict {
			t.Fatalf("expected 409 Conflict on duplicate version publish, got %d", dupStatus)
		}

		// Publishing disallowed namespace on pub-private MUST be rejected (403)
		otherTar, _ := makePubTarball(t, "forbidden_pkg", "1.0.0", "nope")
		nsStatus, _ := publishViaDartFlow(t, httpClient, ts.URL+"/r/pub-private", privToken, otherTar)
		if nsStatus != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for disallowed namespace, got %d", nsStatus)
		}

		// Using pub-private token against pub-public MUST be rejected (403) — registry isolation!
		crossStatus, _ := publishViaDartFlow(t, httpClient, ts.URL+"/r/pub-public", privToken, sharedTar)
		if crossStatus != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden across registries, got %d", crossStatus)
		}

		// Hybrid namespace protection: publish acme_core 1.0.0 locally to pub-public;
		// querying acme_core MUST return local 1.0.0 and NEVER upstream 9.9.9!
		_, pubPublicToken, _ := authSvc.CreateToken(ctx, auth.CreateTokenInput{
			Name:       "public-publisher",
			UserID:     adminUser.ID,
			RegistryID: pubPublic.ID,
			Scopes:     []auth.Permission{auth.PermissionRead, auth.PermissionPublish},
		})
		localAcmeTar, _ := makePubTarball(t, "acme_core", "1.0.0", "internal-trusted")
		acmeStatus, acmeBody := publishViaDartFlow(t, httpClient, ts.URL+"/r/pub-public", pubPublicToken, localAcmeTar)
		if acmeStatus != http.StatusOK {
			t.Fatalf("publish acme_core failed %d: %s", acmeStatus, acmeBody)
		}

		resp, err := httpClient.Get(ts.URL + "/r/pub-public/api/packages/acme_core")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var acmeResp pub.PackageResponse
		_ = json.NewDecoder(resp.Body).Decode(&acmeResp)
		if acmeResp.Latest == nil || acmeResp.Latest.Version != "1.0.0" || len(acmeResp.Versions) != 1 {
			t.Fatalf("hybrid namespace protection violated! Expected only hosted 1.0.0, got %+v", acmeResp)
		}
	})

	// === VERIFICATION 5: Shared Storage Deduplication Without Unauthorized Access ===
	t.Run("DeduplicationWithoutUnauthorizedAccess", func(t *testing.T) {
		// shared_pkg@1.0.0 is already hosted privately in pub-private.
		// Now cache the EXACT SAME bytes via upstream into pub-public!
		dlResp, err := httpClient.Get(ts.URL + "/r/pub-public/packages/shared_pkg/versions/1.0.0.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		dlBytes, _ := io.ReadAll(dlResp.Body)
		_ = dlResp.Body.Close()
		if dlResp.StatusCode != http.StatusOK || !bytes.Equal(dlBytes, sharedTar) {
			t.Fatalf("expected pub-public cached download ok, status=%d", dlResp.StatusCode)
		}

		// Confirm physical deduplication saved at least len(sharedTar) bytes!
		gStats, _ := storageSvc.GetGlobalStats(ctx)
		if gStats.DeduplicationSaved < int64(len(sharedTar)) {
			t.Fatalf("expected physical dedup savings >= %d, got %+v", len(sharedTar), gStats)
		}

		// Anonymous caller MUST NOT access pub-private's archive even though the physical blob is shared with pub-public!
		anonResp, err := httpClient.Get(ts.URL + "/r/pub-private/packages/shared_pkg/versions/1.0.0.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		_ = anonResp.Body.Close()
		if anonResp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized for anonymous access to private deduplicated blob, got %d", anonResp.StatusCode)
		}

		// Purge cache on pub-public -> MUST remove pub-public's cached ref ONLY, leaving pub-private's hosted blob intact!
		purgeReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/registries/"+pubPublic.ID+"/cache/purge", nil)
		purgeReq.Header.Set("Authorization", "Bearer "+adminSessionToken)
		purgeResp, err := httpClient.Do(purgeReq)
		if err != nil {
			t.Fatal(err)
		}
		_ = purgeResp.Body.Close()
		if purgeResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 on cache purge, got %d", purgeResp.StatusCode)
		}

		// Authorized download from pub-private MUST STILL SUCCEED!
		_, privReadTok, _ := authSvc.CreateToken(ctx, auth.CreateTokenInput{
			Name:       "priv-reader",
			UserID:     adminUser.ID,
			RegistryID: pubPrivate.ID,
			Scopes:     []auth.Permission{auth.PermissionRead},
		})
		authReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/r/pub-private/packages/shared_pkg/versions/1.0.0.tar.gz", nil)
		authReq.Header.Set("Authorization", "Bearer "+privReadTok)
		authResp, err := httpClient.Do(authReq)
		if err != nil {
			t.Fatal(err)
		}
		gotPriv, _ := io.ReadAll(authResp.Body)
		_ = authResp.Body.Close()
		if authResp.StatusCode != http.StatusOK || !bytes.Equal(gotPriv, sharedTar) {
			t.Fatalf("private hosted blob was damaged by public cache purge! status=%d", authResp.StatusCode)
		}
	})

	// === VERIFICATION 6: Independent Registry Quota Enforcement ===
	t.Run("IndependentQuotaEnforcement", func(t *testing.T) {
		_, teamATok, _ := authSvc.CreateToken(ctx, auth.CreateTokenInput{
			Name:       "team-a-pub",
			UserID:     adminUser.ID,
			RegistryID: pubTeamA.ID,
			Scopes:     []auth.Permission{auth.PermissionRead, auth.PermissionPublish},
		})
		tarA, _ := makePubTarball(t, "team_a_pkg", "1.0.0", "exceeds-50-byte-quota")
		st, _ := publishViaDartFlow(t, httpClient, ts.URL+"/r/pub-team-a", teamATok, tarA)
		if st != http.StatusInsufficientStorage {
			t.Fatalf("expected 507 Insufficient Storage on pub-team-a quota breach, got %d", st)
		}
	})

	// === VERIFICATION 7: Live Registry Creation & Disable Without Restart ===
	t.Run("LiveAdminCreateAndDisableWithoutRestart", func(t *testing.T) {
		payload := `{"name":"Pub Team B","slug":"pub-team-b","type":"pub","mode":"hosted","visibility":"public","auth_policy":{"allow_anonymous_read":true,"require_auth_publish":true}}`
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/registries", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+adminSessionToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var created struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&created)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated || created.ID == "" {
			t.Fatalf("expected 201 creating pub-team-b live, got %d", resp.StatusCode)
		}

		// Immediately routable without restart
		listResp, err := httpClient.Get(ts.URL + "/r/pub-team-b/api/packages")
		if err != nil {
			t.Fatal(err)
		}
		_ = listResp.Body.Close()
		if listResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 on newly created registry immediately, got %d", listResp.StatusCode)
		}

		// Disable live -> immediately stops serving (503)
		togReq, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/admin/registries/"+created.ID+"/enabled", strings.NewReader(`{"enabled":false}`))
		togReq.Header.Set("Authorization", "Bearer "+adminSessionToken)
		togReq.Header.Set("Content-Type", "application/json")
		togResp, _ := httpClient.Do(togReq)
		_ = togResp.Body.Close()

		disResp, _ := httpClient.Get(ts.URL + "/r/pub-team-b/api/packages")
		_ = disResp.Body.Close()
		if disResp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 on disabled registry, got %d", disResp.StatusCode)
		}
	})
}
