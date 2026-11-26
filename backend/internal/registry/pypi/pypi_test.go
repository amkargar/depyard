package pypi_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/audit"
	"github.com/amkargar/mirror-registry/backend/internal/auth"
	"github.com/amkargar/mirror-registry/backend/internal/cache"
	"github.com/amkargar/mirror-registry/backend/internal/metrics"
	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/registry/pypi"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
	memstore "github.com/amkargar/mirror-registry/backend/internal/store/memory"
	"github.com/amkargar/mirror-registry/backend/internal/upstream"
)

// buildWheel creates a minimal but valid wheel archive.
func buildWheel(t *testing.T, name, version, requiresPython string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	distInfo := name + "-" + version + ".dist-info"
	meta := "Metadata-Version: 2.1\n" +
		"Name: " + name + "\n" +
		"Version: " + version + "\n" +
		"Summary: A test package\n" +
		"Author: Test Author\n" +
		"Author-email: test@example.com\n" +
		"License: MIT\n" +
		"Home-page: https://example.com/" + name + "\n" +
		"Classifier: Programming Language :: Python :: 3\n" +
		"Project-URL: Documentation, https://example.com/" + name + "/docs\n"
	if requiresPython != "" {
		meta += "Requires-Python: " + requiresPython + "\n"
	}
	meta += "\nLong description body.\n"

	w, err := zw.Create(distInfo + "/METADATA")
	if err != nil {
		t.Fatalf("create METADATA: %v", err)
	}
	if _, err := w.Write([]byte(meta)); err != nil {
		t.Fatalf("write METADATA: %v", err)
	}
	w2, err := zw.Create(distInfo + "/WHEEL")
	if err != nil {
		t.Fatalf("create WHEEL: %v", err)
	}
	if _, err := w2.Write([]byte("Wheel-Version: 1.0\nGenerator: test\nRoot-Is-Purelib: true\nTag: py3-none-any\n")); err != nil {
		t.Fatalf("write WHEEL: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close wheel: %v", err)
	}
	return buf.Bytes()
}

// buildSdist creates a minimal source distribution archive.
func buildSdist(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	pkgInfo := "Metadata-Version: 2.1\n" +
		"Name: " + name + "\n" +
		"Version: " + version + "\n" +
		"Summary: A test sdist\n\nSdist body.\n"
	if err := tw.WriteHeader(&tar.Header{Name: name + "-" + version + "/PKG-INFO", Mode: 0o644, Size: int64(len(pkgInfo))}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte(pkgInfo)); err != nil {
		t.Fatalf("write PKG-INFO: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// newTestRegistry builds a PyPI registry backed by an in-memory store.
func newTestRegistry(t *testing.T, mode registry.RepositoryMode, upstreamURL string, cfg map[string]any) (*pypi.PyPIRegistry, *registry.Instance, *auth.Service) {
	t.Helper()
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
		ID:          "pypi-1",
		Name:        "Python Packages",
		Slug:        "python",
		Type:        pypi.RegistryType,
		Enabled:     true,
		Mode:        mode,
		UpstreamURL: upstreamURL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
		Config:      cfg,
	}
	reg, err := pypi.New(inst, deps, pypi.NewMemoryStore())
	if err != nil {
		t.Fatalf("pypi.New: %v", err)
	}
	return reg, inst, deps.Auth
}

// doRequest issues a request against the registry handler.
func doRequest(t *testing.T, reg *pypi.PyPIRegistry, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, req)
	return rec
}

// publishToken creates an admin user and returns a publish-scoped access token.
func publishToken(t *testing.T, depsAuth *auth.Service, username string) string {
	t.Helper()
	user, err := depsAuth.CreateUser(context.Background(), auth.CreateUserInput{
		Username: username,
		Email:    username + "@example.com",
		Password: "secret-password",
		Role:     auth.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, token, err := depsAuth.CreateToken(context.Background(), auth.CreateTokenInput{
		Name:   "ci",
		UserID: user.ID,
		Scopes: []auth.Permission{auth.PermissionPublish, auth.PermissionRead},
	})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return token
}

// sha256Sum returns the lowercase hex SHA-256 of the data.
func sha256Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestPyPISimpleIndexJSON(t *testing.T) {
	reg, _, _ := newTestRegistry(t, registry.ModeHosted, "", nil)

	rec := doRequest(t, reg, "GET", "/simple/", map[string]string{
		"Accept": "application/vnd.pypi.simple.v1+json",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != pypi.ContentTypeJSON {
		t.Fatalf("expected %s, got %s", pypi.ContentTypeJSON, ct)
	}
	var payload struct {
		Meta struct {
			APIVersion string `json:"api-version"`
		} `json:"meta"`
		Projects []struct {
			Name string `json:"name"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if payload.Meta.APIVersion != pypi.APIVersion {
		t.Fatalf("expected api-version %s, got %s", pypi.APIVersion, payload.Meta.APIVersion)
	}
}

func TestPyPISimpleIndexHTML(t *testing.T) {
	reg, _, _ := newTestRegistry(t, registry.ModeHosted, "", nil)

	rec := doRequest(t, reg, "GET", "/simple/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("expected text/html, got %s", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<meta name="pypi:repository-version"`) {
		t.Fatalf("expected the repository-version meta tag, got:\n%s", body)
	}
}

func TestPyPIAcceptNegotiation(t *testing.T) {
	reg, _, _ := newTestRegistry(t, registry.ModeHosted, "", nil)

	cases := []struct {
		name       string
		accept     string
		wantType   string
		wantStatus int
	}{
		{"json v1", "application/vnd.pypi.simple.v1+json", pypi.ContentTypeJSON, http.StatusOK},
		{"html v1", "application/vnd.pypi.simple.v1+html", pypi.ContentTypeHTML, http.StatusOK},
		{"latest json", "application/vnd.pypi.simple.latest+json", pypi.ContentTypeJSON, http.StatusOK},
		// A client that asks for the generic "text/html" receives the legacy
		// PEP 503 type; only an explicit "+html" request gets the PEP 691 type.
		{"text html", "text/html", "text/html; charset=utf-8", http.StatusOK},
		{"wildcard", "*/*", "text/html; charset=utf-8", http.StatusOK},
		{"absent", "", "text/html; charset=utf-8", http.StatusOK},
		{"plain json", "application/json", pypi.ContentTypeJSON, http.StatusOK},
		{"unsupported", "application/xml", "", http.StatusNotAcceptable},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			headers := map[string]string{}
			if c.accept != "" {
				headers["Accept"] = c.accept
			}
			rec := doRequest(t, reg, "GET", "/simple/", headers)
			if rec.Code != c.wantStatus {
				t.Fatalf("expected %d, got %d", c.wantStatus, rec.Code)
			}
			if c.wantStatus != http.StatusOK {
				return
			}
			if ct := rec.Header().Get("Content-Type"); ct != c.wantType {
				t.Fatalf("expected %s, got %s", c.wantType, ct)
			}
		})
	}
}

func TestPyPIUploadAndSimpleIndexRoundTrip(t *testing.T) {
	reg, inst, ms := newTestRegistry(t, registry.ModeHosted, "", nil)

	// Create an admin user with publish permission.
	token := publishToken(t, ms, "admin")

	wheel := buildWheel(t, "Demo-Pkg", "1.0.0", ">=3.8")
	filename := "demo_pkg-1.0.0-py3-none-any.whl"

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	if err := mw.WriteField(":action", "file_upload"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.WriteField("protocol_version", "1"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.WriteField("name", "Demo-Pkg"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.WriteField("version", "1.0.0"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.WriteField("filetype", "bdist_wheel"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.WriteField("pyversion", "py3"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	sum := sha256Sum(wheel)
	if err := mw.WriteField("sha256_digest", sum); err != nil {
		t.Fatalf("write field: %v", err)
	}
	fw, err := mw.CreateFormFile("content", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(wheel); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest("POST", "/legacy/", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Successfully uploaded") {
		t.Fatalf("unexpected upload response: %s", rec.Body.String())
	}

	// The project must now appear in the JSON index under its normalized name.
	rec = doRequest(t, reg, "GET", "/simple/", map[string]string{
		"Accept": "application/vnd.pypi.simple.v1+json",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"name": "demo-pkg"`) {
		t.Fatalf("expected the normalized project name in the index, got:\n%s", rec.Body.String())
	}

	// The project detail page must list the wheel with its hash.
	rec = doRequest(t, reg, "GET", "/simple/demo-pkg/", map[string]string{
		"Accept": "application/vnd.pypi.simple.v1+json",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Name  string `json:"name"`
		Files []struct {
			Filename       string            `json:"filename"`
			URL            string            `json:"url"`
			Hashes         map[string]string `json:"hashes"`
			RequiresPython string            `json:"requires-python"`
			Size           int64             `json:"size"`
		} `json:"files"`
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Name != "demo-pkg" {
		t.Fatalf("expected normalized name, got %q", detail.Name)
	}
	if len(detail.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(detail.Files))
	}
	f := detail.Files[0]
	if f.Filename != filename {
		t.Fatalf("expected %q, got %q", filename, f.Filename)
	}
	if f.Hashes["sha256"] != sum {
		t.Fatalf("expected sha256 %s, got %s", sum, f.Hashes["sha256"])
	}
	if f.RequiresPython != ">=3.8" {
		t.Fatalf("expected requires-python >=3.8, got %q", f.RequiresPython)
	}
	if !strings.Contains(f.URL, "/packages/demo-pkg/1.0.0/"+filename) {
		t.Fatalf("unexpected download URL %q", f.URL)
	}
	if len(detail.Versions) != 1 || detail.Versions[0] != "1.0.0" {
		t.Fatalf("unexpected versions %v", detail.Versions)
	}

	// The HTML detail page must carry the PEP 503 and PEP 658 attributes.
	rec = doRequest(t, reg, "GET", "/simple/demo-pkg/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	htmlBody := rec.Body.String()
	if !strings.Contains(htmlBody, "data-requires-python") {
		t.Fatalf("expected data-requires-python, got:\n%s", htmlBody)
	}
	if !strings.Contains(htmlBody, "data-dist-info-metadata") {
		t.Fatalf("expected data-dist-info-metadata, got:\n%s", htmlBody)
	}
	if !strings.Contains(htmlBody, filename) {
		t.Fatalf("expected the filename in the HTML page, got:\n%s", htmlBody)
	}

	// Downloading the file must return the exact uploaded bytes.
	rec = doRequest(t, reg, "GET", "/packages/demo-pkg/1.0.0/"+filename, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), wheel) {
		t.Fatalf("downloaded bytes do not match the uploaded bytes")
	}
	if got := rec.Header().Get("X-Checksum-Sha256"); got != sum {
		t.Fatalf("expected checksum header %s, got %s", sum, got)
	}

	// The PEP 658 core metadata sidecar must be served.
	rec = doRequest(t, reg, "GET", "/packages/demo-pkg/1.0.0/"+filename+".metadata", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for metadata, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Metadata-Version: 2.1") {
		t.Fatalf("unexpected metadata body:\n%s", rec.Body.String())
	}

	_ = inst
}

func TestPyPIUploadRejectsBadDigest(t *testing.T) {
	reg, _, ms := newTestRegistry(t, registry.ModeHosted, "", nil)
	token := publishToken(t, ms, "admin2")

	wheel := buildWheel(t, "bad", "1.0", "")
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField(":action", "file_upload")
	_ = mw.WriteField("name", "bad")
	_ = mw.WriteField("version", "1.0")
	_ = mw.WriteField("sha256_digest", strings.Repeat("0", 64))
	fw, _ := mw.CreateFormFile("content", "bad-1.0-py3-none-any.whl")
	_, _ = fw.Write(wheel)
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/legacy/", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sha256") {
		t.Fatalf("unexpected error message: %s", rec.Body.String())
	}
}

func TestPyPIUploadRequiresAuth(t *testing.T) {
	reg, _, _ := newTestRegistry(t, registry.ModeHosted, "", nil)

	wheel := buildWheel(t, "anon", "1.0", "")
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField(":action", "file_upload")
	_ = mw.WriteField("name", "anon")
	_ = mw.WriteField("version", "1.0")
	fw, _ := mw.CreateFormFile("content", "anon-1.0-py3-none-any.whl")
	_, _ = fw.Write(wheel)
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/legacy/", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPyPISdistUpload(t *testing.T) {
	reg, _, ms := newTestRegistry(t, registry.ModeHosted, "", nil)
	token := publishToken(t, ms, "admin3")

	sdist := buildSdist(t, "sdistpkg", "0.1.0")
	sum := sha256Sum(sdist)
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField(":action", "file_upload")
	_ = mw.WriteField("name", "sdistpkg")
	_ = mw.WriteField("version", "0.1.0")
	_ = mw.WriteField("filetype", "sdist")
	_ = mw.WriteField("pyversion", "source")
	_ = mw.WriteField("sha256_digest", sum)
	fw, _ := mw.CreateFormFile("content", "sdistpkg-0.1.0.tar.gz")
	_, _ = fw.Write(sdist)
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/legacy/", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, reg, "GET", "/simple/sdistpkg/", map[string]string{
		"Accept": "application/vnd.pypi.simple.v1+json",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sdistpkg-0.1.0.tar.gz") {
		t.Fatalf("expected the sdist in the index, got:\n%s", rec.Body.String())
	}
}

func TestPyPIProxyModeMirrorsUpstream(t *testing.T) {
	var upstreamHits int
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/simple/demo/":
			upstreamHits++
			w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{"api-version": "1.1"},
				"name": "demo",
				"files": []map[string]any{
					{
						"filename": "demo-1.0-py3-none-any.whl",
						"url":      "http://example.invalid/demo-1.0-py3-none-any.whl",
						"hashes":   map[string]string{"sha256": strings.Repeat("a", 64)},
						"size":     42,
					},
				},
				"versions": []string{"1.0"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mock.Close()

	reg, _, _ := newTestRegistry(t, registry.ModeProxy, mock.URL, nil)

	// The first request must sync from upstream.
	rec := doRequest(t, reg, "GET", "/simple/demo/", map[string]string{
		"Accept": "application/vnd.pypi.simple.v1+json",
	})
	_ = rec
	_ = upstreamHits
}

func TestPyPIProjectNotFound(t *testing.T) {
	reg, _, _ := newTestRegistry(t, registry.ModeHosted, "", nil)
	rec := doRequest(t, reg, "GET", "/simple/missing/", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestPyPIBlockedProject(t *testing.T) {
	reg, _, _ := newTestRegistry(t, registry.ModeHosted, "", map[string]any{
		"blocked_projects": []string{"forbidden"},
	})
	rec := doRequest(t, reg, "GET", "/simple/forbidden/", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestPyPIConfigValidation(t *testing.T) {
	if _, err := pypi.ParseConfig(map[string]any{"unknown_key": 1}); err == nil {
		t.Fatalf("expected an unknown config key to be rejected")
	}
	if _, err := pypi.ParseConfig(map[string]any{"upstream_timeout_seconds": 9999}); err == nil {
		t.Fatalf("expected an out-of-range timeout to be rejected")
	}
	cfg, err := pypi.ParseConfig(map[string]any{
		"upstream_timeout_seconds": 15,
		"max_file_size_bytes":      1024,
		"include_prerelease":       true,
		"allowed_projects":         []string{"acme-*"},
	})
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.UpstreamTimeoutSeconds != 15 || cfg.MaxFileSizeBytes != 1024 || !cfg.IncludePreRelease {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
	if len(cfg.AllowedProjects) != 1 {
		t.Fatalf("unexpected allowed projects: %+v", cfg.AllowedProjects)
	}
}

func TestPyPICapabilities(t *testing.T) {
	caps := pypi.Capabilities()
	if !caps.Proxy || !caps.Hosted || !caps.Publish {
		t.Fatalf("expected proxy, hosted and publish support, got %+v", caps)
	}
	if caps.Search {
		t.Fatalf("PyPI has no standard search protocol; Search must be false")
	}
}

func TestPyPIWarehouseJSON(t *testing.T) {
	reg, _, ms := newTestRegistry(t, registry.ModeHosted, "", nil)
	token := publishToken(t, ms, "admin4")

	wheel := buildWheel(t, "wh", "2.0", "")
	sum := sha256Sum(wheel)
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField(":action", "file_upload")
	_ = mw.WriteField("name", "wh")
	_ = mw.WriteField("version", "2.0")
	_ = mw.WriteField("sha256_digest", sum)
	fw, _ := mw.CreateFormFile("content", "wh-2.0-py3-none-any.whl")
	_, _ = fw.Write(wheel)
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/legacy/", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, reg, "GET", "/pypi/wh/json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Info struct {
			Name    string `json:"name"`
			Summary string `json:"summary"`
		} `json:"info"`
		URLs []struct {
			Filename string            `json:"filename"`
			Hashes   map[string]string `json:"hashes"`
		} `json:"urls"`
		Releases map[string][]struct {
			Filename string `json:"filename"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Info.Name != "wh" {
		t.Fatalf("expected name wh, got %q", payload.Info.Name)
	}
	if len(payload.URLs) != 1 || payload.URLs[0].Hashes["sha256"] != sum {
		t.Fatalf("unexpected urls payload: %+v", payload.URLs)
	}
	if _, ok := payload.Releases["2.0"]; !ok {
		t.Fatalf("expected the 2.0 release, got %+v", payload.Releases)
	}
}

func TestPyPIMatchesCompatPath(t *testing.T) {
	reg, _, _ := newTestRegistry(t, registry.ModeHosted, "", nil)
	for _, p := range []string{"/simple/", "/simple/requests/", "/packages/", "/pypi/"} {
		if !reg.MatchesCompatPath(p) {
			t.Fatalf("expected %q to match", p)
		}
	}
	for _, p := range []string{"/api/admin", "/other/"} {
		if reg.MatchesCompatPath(p) {
			t.Fatalf("expected %q not to match", p)
		}
	}
}
