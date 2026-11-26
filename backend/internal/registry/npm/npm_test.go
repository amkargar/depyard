package npm_test

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

	"github.com/amkargar/depyard/backend/internal/audit"
	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/cache"
	"github.com/amkargar/depyard/backend/internal/metrics"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/registry/npm"
	"github.com/amkargar/depyard/backend/internal/storage"
	memstore "github.com/amkargar/depyard/backend/internal/store/memory"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

type testRegistry struct {
	registry *npm.NpmRegistry
	store    *npm.MemoryStore
	auth     *auth.Service
	cache    *cache.Coordinator
	storage  *storage.Service
}

func newTestRegistry(t *testing.T, mode registry.RepositoryMode, upstreamURL string, cfg map[string]any, visibility registry.Visibility) *testRegistry {
	t.Helper()
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
	inst := &registry.Instance{
		ID:          "npm-test",
		Name:        "npm Packages",
		Slug:        "npm-public",
		Type:        npm.RegistryType,
		Enabled:     true,
		Mode:        mode,
		UpstreamURL: upstreamURL,
		Visibility:  visibility,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: visibility == registry.VisibilityPublic},
		Config:      cfg,
	}
	store := npm.NewMemoryStore()
	reg, err := npm.New(inst, deps, store)
	if err != nil {
		t.Fatalf("npm.New: %v", err)
	}
	return &testRegistry{registry: reg, store: store, auth: authSvc, cache: cacheCoord, storage: storageSvc}
}

func (tr *testRegistry) request(method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req = req.WithContext(registry.WithBaseURL(req.Context(), "http://depyard.test/r/npm-public"))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	tr.registry.Handler().ServeHTTP(rec, req)
	return rec
}

func tokenFor(t *testing.T, svc *auth.Service, name string, scopes ...auth.Permission) string {
	t.Helper()
	user, err := svc.CreateUser(context.Background(), auth.CreateUserInput{
		Username: name, Email: name + "@example.com", Password: "a-very-secret-password", Role: auth.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, token, err := svc.CreateToken(context.Background(), auth.CreateTokenInput{
		Name: name, UserID: user.ID, Scopes: scopes,
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	return token
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// fakeNpmUpstream serves a packument and a tarball for "left-pad".
type fakeNpmUpstream struct {
	server    *httptest.Server
	tarball   []byte
	fileHits  int
	packument map[string]any
}

func newFakeNpmUpstream(t *testing.T) *fakeNpmUpstream {
	t.Helper()
	up := &fakeNpmUpstream{tarball: []byte("fake npm tarball payload")}
	sha1Sum := sha1.Sum(up.tarball)
	sha512Sum := sha512.Sum512(up.tarball)
	up.packument = map[string]any{
		"name":        "left-pad",
		"description": "String left pad",
		"dist-tags":   map[string]string{"latest": "1.0.0"},
		"time":        map[string]string{"1.0.0": "2020-01-01T00:00:00Z"},
		"versions": map[string]any{
			"1.0.0": map[string]any{
				"name": "left-pad", "version": "1.0.0", "description": "String left pad",
				"main": "index.js",
				"dist": map[string]any{
					"tarball":   "",
					"shasum":    hex.EncodeToString(sha1Sum[:]),
					"integrity": "sha512-" + base64.StdEncoding.EncodeToString(sha512Sum[:]),
				},
			},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /left-pad", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		version := up.packument["versions"].(map[string]any)["1.0.0"].(map[string]any)
		version["dist"].(map[string]any)["tarball"] = up.server.URL + "/left-pad/-/left-pad-1.0.0.tgz"
		_ = json.NewEncoder(w).Encode(up.packument)
	})
	mux.HandleFunc("GET /left-pad/-/left-pad-1.0.0.tgz", func(w http.ResponseWriter, r *http.Request) {
		up.fileHits++
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(up.tarball)
	})
	up.server = httptest.NewServer(mux)
	t.Cleanup(up.server.Close)
	return up
}

func TestNpmProxyPackumentAndTarballCaching(t *testing.T) {
	up := newFakeNpmUpstream(t)
	tr := newTestRegistry(t, registry.ModeProxy, up.server.URL, nil, registry.VisibilityPublic)

	rec := tr.request(http.MethodGet, "/left-pad", nil, map[string]string{"Accept": npm.ContentTypeAbbreviated})
	if rec.Code != http.StatusOK {
		t.Fatalf("packument: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, npm.ContentTypeAbbreviated) {
		t.Fatalf("expected abbreviated content type, got %q", ct)
	}
	var doc struct {
		Name     string                    `json:"name"`
		DistTags map[string]string         `json:"dist-tags"`
		Versions map[string]map[string]any `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode packument: %v", err)
	}
	if doc.Name != "left-pad" || doc.DistTags["latest"] != "1.0.0" {
		t.Fatalf("unexpected packument: %+v", doc)
	}
	dist, ok := doc.Versions["1.0.0"]["dist"].(map[string]any)
	if !ok {
		t.Fatalf("missing dist in abbreviated packument: %s", rec.Body.String())
	}
	if got := dist["tarball"]; got != "http://depyard.test/r/npm-public/left-pad/-/left-pad-1.0.0.tgz" {
		t.Fatalf("tarball URL was not rewritten to the mirror: %v", got)
	}
	if _, hasDescription := doc.Versions["1.0.0"]["description"]; hasDescription {
		t.Fatal("abbreviated packument must not include full version metadata")
	}

	for i, wantCache := range []string{"MISS", "HIT"} {
		rec := tr.request(http.MethodGet, "/left-pad/-/left-pad-1.0.0.tgz", nil, nil)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), up.tarball) {
			t.Fatalf("download %d: expected tarball, got %d: %s", i, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Registry-Cache"); got != wantCache {
			t.Fatalf("download %d: expected cache %s, got %s", i, wantCache, got)
		}
	}
	if up.fileHits != 1 {
		t.Fatalf("expected exactly one upstream tarball fetch, got %d", up.fileHits)
	}

	pkg, err := tr.store.GetPackage(context.Background(), "npm-test", "left-pad")
	if err != nil || len(pkg.Versions) != 1 || pkg.Versions[0].Digest == "" {
		t.Fatalf("expected cached version with digest, got %+v err=%v", pkg, err)
	}
}

func publishPayload(t *testing.T, name, version string, tarball []byte, shasum, integrity string) []byte {
	t.Helper()
	manifest := map[string]any{
		"name": name, "version": version, "description": "Published locally",
		"main": "index.js",
		"dist": map[string]any{"shasum": shasum, "integrity": integrity},
	}
	raw, _ := json.Marshal(manifest)
	body := map[string]any{
		"name": name, "description": "Published locally",
		"dist-tags": map[string]string{"latest": version},
		"versions":  map[string]any{version: json.RawMessage(raw)},
		"_attachments": map[string]any{
			name + "-" + version + ".tgz": map[string]any{
				"data_type": "application/octet-stream",
				"data":      base64.StdEncoding.EncodeToString(tarball),
				"length":    len(tarball),
			},
		},
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode publish payload: %v", err)
	}
	return out
}

func TestNpmPublishDownloadAndUnpublish(t *testing.T) {
	tr := newTestRegistry(t, registry.ModeHosted, "", nil, registry.VisibilityPublic)
	tarball := []byte("locally published tarball")
	sha1Sum := sha1.Sum(tarball)
	sha512Sum := sha512.Sum512(tarball)
	shasum := hex.EncodeToString(sha1Sum[:])
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(sha512Sum[:])
	payload := publishPayload(t, "@acme/tools", "1.2.3", tarball, shasum, integrity)

	// Publishing without credentials is refused with an npm-style challenge.
	rec := tr.request(http.MethodPut, "/@acme%2Ftools", payload, nil)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("expected 401 with WWW-Authenticate, got %d: %s", rec.Code, rec.Body.String())
	}
	publishToken := tokenFor(t, tr.auth, "publisher", auth.PermissionPublish, auth.PermissionRead)
	rec = tr.request(http.MethodPut, "/@acme%2Ftools", payload, bearer(publishToken))
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = tr.request(http.MethodGet, "/@acme%2Ftools", nil, map[string]string{"Accept": npm.ContentTypeJSON})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "http://depyard.test/r/npm-public/@acme%2Ftools/-/acme-tools-1.2.3.tgz") {
		t.Fatalf("packument: got %d: %s", rec.Code, rec.Body.String())
	}
	rec = tr.request(http.MethodGet, "/@acme%2Ftools/-/acme-tools-1.2.3.tgz", nil, nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), tarball) {
		t.Fatalf("download: got %d: %s", rec.Code, rec.Body.String())
	}

	// npm forbids publishing over an immutable version.
	rec = tr.request(http.MethodPut, "/@acme%2Ftools", payload, bearer(publishToken))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("duplicate publish: expected 403, got %d: %s", rec.Code, rec.Body.String())
	}

	deleteToken := tokenFor(t, tr.auth, "deleter", auth.PermissionDelete, auth.PermissionRead)
	rec = tr.request(http.MethodDelete, "/@acme%2Ftools?version=1.2.3", nil, bearer(deleteToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("unpublish: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = tr.request(http.MethodGet, "/@acme%2Ftools", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("after unpublish: expected 404, got %d", rec.Code)
	}

	// npm unpublish of a whole package uses DELETE /{package}/-rev/<revision>.
	if rec := tr.request(http.MethodPut, "/@acme%2Ftools", payload, bearer(publishToken)); rec.Code != http.StatusCreated {
		t.Fatalf("republish: got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := tr.request(http.MethodDelete, "/@acme%2Ftools/-rev/undefined", nil, bearer(deleteToken)); rec.Code != http.StatusOK {
		t.Fatalf("unpublish whole package: got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNpmPublishRejectsWrongIntegrity(t *testing.T) {
	tr := newTestRegistry(t, registry.ModeHosted, "", nil, registry.VisibilityPublic)
	payload := publishPayload(t, "broken", "1.0.0", []byte("tarball"), strings.Repeat("0", 40), "sha512-"+base64.StdEncoding.EncodeToString(make([]byte, 64)))
	token := tokenFor(t, tr.auth, "publisher2", auth.PermissionPublish, auth.PermissionRead)
	rec := tr.request(http.MethodPut, "/broken", payload, bearer(token))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "digest") {
		t.Fatalf("expected integrity rejection, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNpmPrivateRegistryRequiresReadPermission(t *testing.T) {
	tr := newTestRegistry(t, registry.ModeHosted, "", nil, registry.VisibilityPrivate)
	rec := tr.request(http.MethodGet, "/private-pkg", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for anonymous read, got %d", rec.Code)
	}
	readToken := tokenFor(t, tr.auth, "reader", auth.PermissionRead)
	rec = tr.request(http.MethodGet, "/private-pkg", nil, bearer(readToken))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("authorized read of an unknown package should be 404, got %d", rec.Code)
	}
}

func TestNpmAdminOperationsAndPolicies(t *testing.T) {
	tr := newTestRegistry(t, registry.ModeHybrid, "", map[string]any{
		"blocked_packages": []string{"evil-*"},
	}, registry.VisibilityPublic)

	// A blocked package cannot be published.
	token := tokenFor(t, tr.auth, "publisher3", auth.PermissionPublish, auth.PermissionRead)
	payload := publishPayload(t, "evil-lib", "1.0.0", []byte("bad"), "", "")
	if rec := tr.request(http.MethodPut, "/evil-lib", payload, bearer(token)); rec.Code != http.StatusForbidden {
		t.Fatalf("expected blocked package rejection, got %d: %s", rec.Code, rec.Body.String())
	}

	payload = publishPayload(t, "acme-lib", "2.0.0", []byte("good"), "", "")
	if rec := tr.request(http.MethodPut, "/acme-lib", payload, bearer(token)); rec.Code != http.StatusCreated {
		t.Fatalf("publish: got %d: %s", rec.Code, rec.Body.String())
	}
	page, err := tr.registry.ListArtifacts(context.Background(), registry.ArtifactQuery{Page: 1, Limit: 10})
	if err != nil || page.Total != 1 || page.Items[0].Name != "acme-lib" {
		t.Fatalf("ListArtifacts: got %+v err=%v", page, err)
	}
	if err := tr.registry.DeleteArtifact(context.Background(), "acme-lib", "2.0.0"); err != nil {
		t.Fatalf("DeleteArtifact: %v", err)
	}
	page, _ = tr.registry.ListArtifacts(context.Background(), registry.ArtifactQuery{Page: 1, Limit: 10})
	if page.Total != 0 {
		t.Fatalf("expected empty artifact list after deletion, got %+v", page)
	}

	if _, err := tr.registry.PurgeCache(context.Background()); err != nil {
		t.Fatalf("PurgeCache: %v", err)
	}
	health := tr.registry.CheckHealth(context.Background())
	if !health.Available {
		t.Fatalf("hosted/hybrid instance without upstream should be healthy: %+v", health)
	}
}

func TestNpmExplorerAPIReturnsPackageVersions(t *testing.T) {
	tr := newTestRegistry(t, registry.ModeHosted, "", nil, registry.VisibilityPublic)
	token := tokenFor(t, tr.auth, "publisher-explorer", auth.PermissionPublish, auth.PermissionRead)
	payload := publishPayload(t, "@acme/ui", "1.1.0", []byte("tarball"), "", "")
	if rec := tr.request(http.MethodPut, "/@acme%2Fui", payload, bearer(token)); rec.Code != http.StatusCreated {
		t.Fatalf("publish: got %d: %s", rec.Code, rec.Body.String())
	}

	for _, path := range []string{"/api/packages", "/api/packages/@acme%2Fui", "/api/packages/@acme/ui"} {
		rec := tr.request(http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("explorer %s: got %d: %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := tr.request(http.MethodGet, "/api/packages/@acme%2Fui", nil, nil)
	var detail struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
		Versions []struct {
			Version string `json:"version"`
			URL     string `json:"url"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode explorer detail: %v", err)
	}
	if detail.Package.Name != "@acme/ui" || len(detail.Versions) != 1 || detail.Versions[0].Version != "1.1.0" {
		t.Fatalf("unexpected explorer detail: %+v", detail)
	}
}

func TestNpmConfigValidationThroughRegistration(t *testing.T) {
	reg := npm.Registration(npm.NewMemoryStore())
	if reg.Type != npm.RegistryType || reg.Create == nil {
		t.Fatalf("unexpected registration: %+v", reg)
	}
	if err := reg.ValidateConfig(map[string]any{"bad_field": 1}); err == nil {
		t.Fatal("unknown config fields must be rejected")
	}
	if err := reg.ValidateConfig(map[string]any{"upstream_timeout_seconds": 9999}); err == nil {
		t.Fatal("out-of-range timeout must be rejected")
	}
	if err := reg.ValidateConfig(map[string]any{"include_prerelease": true, "max_tarball_size_bytes": 1024}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if !reg.Capabilities.Proxy || !reg.Capabilities.Hosted || !reg.Capabilities.Publish {
		t.Fatalf("expected proxy, hosted and publish capabilities: %+v", reg.Capabilities)
	}
}
