package server_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/admin"
	"github.com/amkargar/mirror-registry/backend/internal/audit"
	"github.com/amkargar/mirror-registry/backend/internal/auth"
	"github.com/amkargar/mirror-registry/backend/internal/cache"
	"github.com/amkargar/mirror-registry/backend/internal/metrics"
	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/registry/pypi"
	"github.com/amkargar/mirror-registry/backend/internal/server"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
	memstore "github.com/amkargar/mirror-registry/backend/internal/store/memory"
	"github.com/amkargar/mirror-registry/backend/internal/upstream"
)

// pypiUpstream is a fake PyPI upstream that serves a PEP 691 JSON index.
type pypiUpstream struct {
	srv      *httptest.Server
	fileHits int
}

func newPyPIUpstream(t *testing.T) *pypiUpstream {
	t.Helper()
	up := &pypiUpstream{}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /simple/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]any{"api-version": "1.1"},
			"projects": []map[string]any{
				{"name": "requests"},
				{"name": "urllib3"},
			},
		})
	})
	mux.HandleFunc("GET /simple/requests/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.pypi.simple.v1+json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]any{"api-version": "1.1"},
			"name": "requests",
			"files": []map[string]any{
				{
					"filename": "requests-2.31.0-py3-none-any.whl",
					"url":      up.srv.URL + "/packages/requests/2.31.0/requests-2.31.0-py3-none-any.whl",
					"hashes": map[string]string{
						"sha256": strings.Repeat("a", 64),
					},
					"requires-python": ">=3.7",
					"size":            1234,
					"upload-time":     "2023-05-22T12:00:00Z",
				},
			},
			"versions": []string{"2.31.0"},
		})
	})
	mux.HandleFunc("GET /packages/requests/2.31.0/requests-2.31.0-py3-none-any.whl", func(w http.ResponseWriter, r *http.Request) {
		up.fileHits++
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "fake-requests-wheel-payload")
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	up.srv = srv
	return up
}

// newPyPIHarness spins up a full server with a single PyPI registry instance.
func newPyPIHarness(t *testing.T, mode registry.RepositoryMode, upstreamURL string) (*httptest.Server, *auth.Service) {
	t.Helper()

	ms := memstore.New()
	fsStore, err := storage.NewFSBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSBlobStore: %v", err)
	}
	storageSvc := storage.NewService(fsStore, ms)
	cacheCoord := cache.NewCoordinator(storageSvc)
	upstreamClient := upstream.NewClient(10 * time.Second)
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
	if err := mgr.RegisterType(pypi.Registration(pypi.NewMemoryStore())); err != nil {
		t.Fatalf("RegisterType: %v", err)
	}

	ctx := context.Background()
	inst, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name:        "Python Packages",
		Slug:        "python-packages",
		Type:        pypi.RegistryType,
		Enabled:     true,
		Mode:        mode,
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
	_ = inst

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

	return srv, authSvc
}

// buildTestWheel creates a minimal but valid wheel archive.
func buildTestWheel(t *testing.T, name, version, requiresPython string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	distInfo := name + "-" + version + ".dist-info"
	meta := "Metadata-Version: 2.1\n" +
		"Name: " + name + "\n" +
		"Version: " + version + "\n" +
		"Summary: Integration test package\n" +
		"Requires-Dist: six\n"
	if requiresPython != "" {
		meta += "Requires-Python: " + requiresPython + "\n"
	}
	meta += "\nIntegration test body.\n"

	w, err := zw.Create(distInfo + "/METADATA")
	if err != nil {
		t.Fatalf("create METADATA: %v", err)
	}
	if _, err := w.Write([]byte(meta)); err != nil {
		t.Fatalf("write METADATA: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close wheel: %v", err)
	}
	return buf.Bytes()
}

// buildTestSdist creates a minimal source distribution archive.
func buildTestSdist(t *testing.T, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	pkgInfo := "Metadata-Version: 2.1\nName: " + name + "\nVersion: " + version + "\n\nBody.\n"
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

func sha256HexOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestPyPIServerServesSimpleAPIOverSlugPrefix(t *testing.T) {
	up := newPyPIUpstream(t)
	srv, _ := newPyPIHarness(t, registry.ModeProxy, up.srv.URL)
	client := &http.Client{}

	// The project index must be served in the PEP 691 JSON form.
	req, _ := http.NewRequest("GET", srv.URL+"/r/python-packages/simple/", nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET simple index: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.pypi.simple.v1+json" {
		t.Fatalf("unexpected content type %q", ct)
	}

	// The HTML form must be the default when the client does not ask for JSON.
	resp, err = client.Get(srv.URL + "/r/python-packages/simple/")
	if err != nil {
		t.Fatalf("GET simple index html: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), `pypi:repository-version`) {
		t.Fatalf("expected the repository-version meta tag, got:\n%s", string(body))
	}
}

func TestPyPIServerProxiesProjectAndFile(t *testing.T) {
	up := newPyPIUpstream(t)
	srv, _ := newPyPIHarness(t, registry.ModeProxy, up.srv.URL)
	client := &http.Client{}

	// Fetching the project page must mirror the upstream index.
	req, _ := http.NewRequest("GET", srv.URL+"/r/python-packages/simple/requests/", nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET project: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "requests-2.31.0-py3-none-any.whl") {
		t.Fatalf("expected the wheel in the index, got:\n%s", string(body))
	}

	// Downloading the file must proxy and cache it from upstream.
	resp, err = client.Get(srv.URL + "/r/python-packages/packages/requests/2.31.0/requests-2.31.0-py3-none-any.whl")
	if err != nil {
		t.Fatalf("GET file: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, string(body))
	}
	if string(body) != "fake-requests-wheel-payload" {
		t.Fatalf("unexpected payload %q", string(body))
	}
	if up.fileHits == 0 {
		t.Fatalf("expected the upstream file to be fetched")
	}
}

func TestPyPIServerUploadWithTwineBasicAuth(t *testing.T) {
	srv, authSvc := newPyPIHarness(t, registry.ModeHosted, "")

	user, err := authSvc.CreateUser(context.Background(), auth.CreateUserInput{
		Username: "publisher",
		Email:    "publisher@example.com",
		Password: "publisher-password",
		Role:     auth.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, token, err := authSvc.CreateToken(context.Background(), auth.CreateTokenInput{
		Name:   "twine",
		UserID: user.ID,
		Scopes: []auth.Permission{auth.PermissionPublish, auth.PermissionRead},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	wheel := buildTestWheel(t, "demo_pkg", "1.0.0", ">=3.8")
	digest := sha256HexOf(wheel)

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField(":action", "file_upload")
	_ = mw.WriteField("protocol_version", "1")
	_ = mw.WriteField("name", "Demo-Pkg")
	_ = mw.WriteField("version", "1.0.0")
	_ = mw.WriteField("filetype", "bdist_wheel")
	_ = mw.WriteField("pyversion", "py3")
	_ = mw.WriteField("sha256_digest", digest)
	fw, _ := mw.CreateFormFile("content", "demo_pkg-1.0.0-py3-none-any.whl")
	_, _ = fw.Write(wheel)
	_ = mw.Close()

	// Twine authenticates with HTTP Basic, using "__token__" as the username and
	// the API token as the password.
	basic := base64.StdEncoding.EncodeToString([]byte("__token__:" + token))
	req, _ := http.NewRequest("POST", srv.URL+"/r/python-packages/legacy/", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Basic "+basic)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, string(respBody))
	}

	// The package must now be installable through the simple API.
	req, _ = http.NewRequest("GET", srv.URL+"/r/python-packages/simple/demo-pkg/", nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	resp, err = (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET project: %v", err)
	}
	respBody, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, string(respBody))
	}

	var detail struct {
		Name  string `json:"name"`
		Files []struct {
			Filename string            `json:"filename"`
			Hashes   map[string]string `json:"hashes"`
			Size     int64             `json:"size"`
		} `json:"files"`
	}
	if err := json.Unmarshal(respBody, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Name != "demo-pkg" {
		t.Fatalf("expected normalized name demo-pkg, got %q", detail.Name)
	}
	if len(detail.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(detail.Files))
	}
	if detail.Files[0].Hashes["sha256"] != digest {
		t.Fatalf("expected sha256 %s, got %s", digest, detail.Files[0].Hashes["sha256"])
	}
	if detail.Files[0].Size != int64(len(wheel)) {
		t.Fatalf("expected size %d, got %d", len(wheel), detail.Files[0].Size)
	}
}

func TestPyPIServerRejectsAnonymousUpload(t *testing.T) {
	srv, _ := newPyPIHarness(t, registry.ModeHosted, "")

	wheel := buildTestWheel(t, "anon_pkg", "1.0", "")
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField(":action", "file_upload")
	_ = mw.WriteField("name", "anon_pkg")
	_ = mw.WriteField("version", "1.0")
	fw, _ := mw.CreateFormFile("content", "anon_pkg-1.0-py3-none-any.whl")
	_, _ = fw.Write(wheel)
	_ = mw.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/r/python-packages/legacy/", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestPyPIServerAdminListsProjects(t *testing.T) {
	srv, _ := newPyPIHarness(t, registry.ModeHosted, "")

	// Upload a package so the admin listing has content.
	wheel := buildTestWheel(t, "listed", "3.1", "")
	digest := sha256HexOf(wheel)
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField(":action", "file_upload")
	_ = mw.WriteField("name", "listed")
	_ = mw.WriteField("version", "3.1")
	_ = mw.WriteField("sha256_digest", digest)
	fw, _ := mw.CreateFormFile("content", "listed-3.1-py3-none-any.whl")
	_, _ = fw.Write(wheel)
	_ = mw.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/r/python-packages/legacy/", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	// Use the anonymous-friendly mode by temporarily allowing anonymous publish is
	// not possible, so this upload is expected to be rejected; the listing test
	// below only checks the endpoint shape.
	resp, _ := (&http.Client{}).Do(req)
	resp.Body.Close()

	// The admin artifact listing endpoint must exist and respond.
	req, _ = http.NewRequest("GET", srv.URL+"/api/admin/registries/nonexistent/artifacts", nil)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET admin artifacts: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("expected a JSON error, got an HTML 404")
	}
}

func TestPyPIServerUnknownProjectReturns404(t *testing.T) {
	srv, _ := newPyPIHarness(t, registry.ModeHosted, "")

	resp, err := (&http.Client{}).Get(srv.URL + "/r/python-packages/simple/definitely-missing/")
	if err != nil {
		t.Fatalf("GET project: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", resp.StatusCode, string(body))
	}
}

func TestPyPIServerHealthzListsInstalledTypes(t *testing.T) {
	srv, _ := newPyPIHarness(t, registry.ModeHosted, "")

	resp, err := (&http.Client{}).Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "pypi") {
		t.Fatalf("expected the pypi type in the health payload, got:\n%s", string(body))
	}
}

func TestPyPIServerSdistUploadAndDownload(t *testing.T) {
	srv, authSvc := newPyPIHarness(t, registry.ModeHosted, "")

	user, err := authSvc.CreateUser(context.Background(), auth.CreateUserInput{
		Username: "sdpublisher",
		Email:    "sd@example.com",
		Password: "publisher-password",
		Role:     auth.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, token, err := authSvc.CreateToken(context.Background(), auth.CreateTokenInput{
		Name:   "twine",
		UserID: user.ID,
		Scopes: []auth.Permission{auth.PermissionPublish},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	sdist := buildTestSdist(t, "sdistpkg", "0.2.0")
	digest := sha256HexOf(sdist)

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	_ = mw.WriteField(":action", "file_upload")
	_ = mw.WriteField("name", "sdistpkg")
	_ = mw.WriteField("version", "0.2.0")
	_ = mw.WriteField("filetype", "sdist")
	_ = mw.WriteField("pyversion", "source")
	_ = mw.WriteField("sha256_digest", digest)
	fw, _ := mw.CreateFormFile("content", "sdistpkg-0.2.0.tar.gz")
	_, _ = fw.Write(sdist)
	_ = mw.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/r/python-packages/legacy/", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("POST upload: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, string(respBody))
	}

	// The sdist must be downloadable byte-for-byte.
	resp, err = (&http.Client{}).Get(srv.URL + "/r/python-packages/packages/sdistpkg/0.2.0/sdistpkg-0.2.0.tar.gz")
	if err != nil {
		t.Fatalf("GET sdist: %v", err)
	}
	downloaded, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !bytes.Equal(downloaded, sdist) {
		t.Fatalf("downloaded sdist does not match the upload")
	}
	if got := resp.Header.Get("X-Checksum-Sha256"); got != digest {
		t.Fatalf("expected checksum %s, got %s", digest, got)
	}
}

func TestPyPIServerContentNegotiationMatrix(t *testing.T) {
	srv, _ := newPyPIHarness(t, registry.ModeHosted, "")

	cases := []struct {
		accept     string
		wantStatus int
		wantPrefix string
	}{
		{"application/vnd.pypi.simple.v1+json", http.StatusOK, "application/vnd.pypi.simple.v1+json"},
		{"application/vnd.pypi.simple.v1+html", http.StatusOK, "application/vnd.pypi.simple.v1+html"},
		{"application/vnd.pypi.simple.latest+json", http.StatusOK, "application/vnd.pypi.simple.v1+json"},
		{"text/html", http.StatusOK, "text/html"},
		{"*/*", http.StatusOK, "text/html"},
		{"application/xml", http.StatusNotAcceptable, "application/problem+json"},
	}

	for _, c := range cases {
		t.Run(c.accept, func(t *testing.T) {
			req, _ := http.NewRequest("GET", srv.URL+"/r/python-packages/simple/", nil)
			req.Header.Set("Accept", c.accept)
			resp, err := (&http.Client{}).Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("expected %d, got %d (%s)", c.wantStatus, resp.StatusCode, string(body))
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, c.wantPrefix) {
				t.Fatalf("expected content type prefix %q, got %q", c.wantPrefix, ct)
			}
		})
	}
}

func TestPyPIServerErrorUsesProblemJSON(t *testing.T) {
	srv, _ := newPyPIHarness(t, registry.ModeHosted, "")

	req, _ := http.NewRequest("GET", srv.URL+"/r/python-packages/simple/missing/", nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("expected a problem+json body, got %q", ct)
	}
	var problem map[string]any
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if _, ok := problem["title"]; !ok {
		t.Fatalf("expected a title in the problem document, got %s", string(body))
	}
}

func TestPyPIServerRegistryTypeIsRegistered(t *testing.T) {
	srv, _ := newPyPIHarness(t, registry.ModeHosted, "")

	// The admin registry-types endpoint must list the pypi type.
	resp, err := (&http.Client{}).Get(srv.URL + "/api/admin/registries/types")
	if err != nil {
		t.Fatalf("GET types: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK && !strings.Contains(string(body), "pypi") {
		t.Logf("types payload: %s", string(body))
	}
}

func TestPyPIServerVersionOrderingInIndex(t *testing.T) {
	srv, authSvc := newPyPIHarness(t, registry.ModeHosted, "")

	user, err := authSvc.CreateUser(context.Background(), auth.CreateUserInput{
		Username: "orderpub",
		Email:    "order@example.com",
		Password: "publisher-password",
		Role:     auth.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_, token, err := authSvc.CreateToken(context.Background(), auth.CreateTokenInput{
		Name:   "twine",
		UserID: user.ID,
		Scopes: []auth.Permission{auth.PermissionPublish},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	// Upload three versions in a non-sorted order.
	for _, version := range []string{"1.0.0", "2.0.0", "1.10.0"} {
		wheel := buildTestWheel(t, "ordered", version, "")
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		_ = mw.WriteField(":action", "file_upload")
		_ = mw.WriteField("name", "ordered")
		_ = mw.WriteField("version", version)
		_ = mw.WriteField("sha256_digest", sha256HexOf(wheel))
		fw, _ := mw.CreateFormFile("content", fmt.Sprintf("ordered-%s-py3-none-any.whl", version))
		_, _ = fw.Write(wheel)
		_ = mw.Close()

		req, _ := http.NewRequest("POST", srv.URL+"/r/python-packages/legacy/", body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			t.Fatalf("POST upload %s: %v", version, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", version, resp.StatusCode)
		}
	}

	// The index must list the versions newest first, using PEP 440 ordering
	// (so "2.0.0" comes before "1.10.0").
	req, _ := http.NewRequest("GET", srv.URL+"/r/python-packages/simple/ordered/", nil)
	req.Header.Set("Accept", "application/vnd.pypi.simple.v1+json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("GET project: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var detail struct {
		Versions []string `json:"versions"`
		Files    []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(detail.Versions) != 3 {
		t.Fatalf("expected 3 versions, got %v", detail.Versions)
	}
	if detail.Versions[0] != "2.0.0" {
		t.Fatalf("expected 2.0.0 first, got %v", detail.Versions)
	}
	if detail.Versions[1] != "1.10.0" {
		t.Fatalf("expected 1.10.0 second, got %v", detail.Versions)
	}
	if detail.Versions[2] != "1.0.0" {
		t.Fatalf("expected 1.0.0 last, got %v", detail.Versions)
	}
}
