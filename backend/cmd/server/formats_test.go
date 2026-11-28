package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// All requests cross the real slug router, shared authorization and filesystem
// CAS. Fake upstreams keep the contract tests deterministic and offline.
type fixture struct {
	t             *testing.T
	mux           http.Handler
	prefix, token string
	mgr           *registry.Manager
	inst          *registry.Instance
	deps          registry.ServiceDeps
}

func newFixture(t *testing.T, kind string, mode registry.RepositoryMode, up string) *fixture {
	t.Helper()
	ms := memstore.New()
	fs, err := storage.NewFSBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := storage.NewService(fs, ms)
	deps := registry.ServiceDeps{
		Storage:  svc,
		Cache:    cache.NewCoordinator(svc),
		Auth:     auth.NewService(ms, time.Hour),
		Metrics:  metrics.NewCollector(ms),
		Upstream: upstream.NewClient(time.Second),
	}
	mgr := registry.NewManager(ms, deps)
	for _, reg := range append(objectFormats(), goproxy.Registration(goproxy.NewMemoryStore())) {
		if err = mgr.RegisterType(reg); err != nil {
			t.Fatal(err)
		}
	}
	inst, err := mgr.CreateInstance(context.Background(), &registry.Instance{
		Type:        kind,
		Name:        "Test",
		Slug:        "private-builds",
		Mode:        mode,
		UpstreamURL: up,
		Enabled:     true,
		IsDefault:   true,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
		CachePolicy: registry.DefaultCachePolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := deps.Auth.CreateToken(context.Background(), auth.CreateTokenInput{Name: "builder", RegistryID: inst.ID, Scopes: []auth.Permission{auth.PermissionPublish, auth.PermissionRead}})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		t:      t,
		mux:    server.New(server.Options{Manager: mgr, PublicBaseURL: "https://registry.test"}),
		prefix: "/r/" + inst.Slug,
		token:  token,
		mgr:    mgr,
		inst:   inst,
		deps:   deps,
	}
}

func (f *fixture) req(method, p, body string, headers map[string]string, code int) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, f.prefix+p, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	if w.Code != code {
		f.t.Fatalf("%s %s = %d, want %d (%s)", method, p, w.Code, code, w.Body.String())
	}
	return w
}

func (f *fixture) write(method, p, body string, code int) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.req(method, p, body, map[string]string{"Authorization": "Bearer " + f.token}, code)
}

func (f *fixture) get(p string, code int) string {
	f.t.Helper()
	return f.req("GET", p, "", nil, code).Body.String()
}

func digest(s string) string { d := sha256.Sum256([]byte(s)); return hex.EncodeToString(d[:]) }
func zipData(t *testing.T, name, data string) string {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(w, data); err != nil {
		t.Fatal(err)
	}
	if err = zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestRepositoryLayouts(t *testing.T) {
	for kind, path := range map[string]string{
		"alpine":   "/v3.23/main/x86_64/APKINDEX.tar.gz",
		"conda":    "/linux-64/repodata.json",
		"cran":     "/src/contrib/PACKAGES.gz",
		"debian":   "/dists/stable/InRelease",
		"ivy":      "/org.acme/example/ivy-1.0.xml",
		"maven":    "/org/acme/example/1.0/example-1.0.pom",
		"nix":      "/nix-cache-info",
		"pacman":   "/core/os/x86_64/core.db",
		"rpm":      "/repodata/repomd.xml",
		"rubygems": "/specs.4.8.gz",
	} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/origin"+path || r.Method != "GET" || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
				}
				io.WriteString(w, "index\x00upstream")
			}))
			defer up.Close()
			f := newFixture(t, kind, registry.ModeHybrid, up.URL+"/origin")
			if f.get(path, 200) != "index\x00upstream" || f.get(path, 200) != "index\x00upstream" || calls.Load() != 1 {
				t.Fatal("read-through cache miss")
			}
			f.req("PUT", path, "no", nil, 401)
			f.write("PUT", path, "signed\x00hosted", 201)
			f.deps.Cache.PurgeRegistry(context.Background(), f.inst.ID)
			if f.get(path, 200) != "signed\x00hosted" || calls.Load() != 1 {
				t.Fatal("protected local file lost")
			}
			w := f.req("GET", path, "", map[string]string{"Range": "bytes=0-5"}, 206)
			if w.Body.String() != "signed" {
				t.Fatal("bad range")
			}
			f.req("HEAD", path, "", map[string]string{"If-None-Match": w.Header().Get("ETag")}, 304)
		})
	}
}

func TestRepositoryModeAndScope(t *testing.T) {
	h := newFixture(t, "maven", registry.ModeHosted, "")
	h.get("/absent", 404)
	h.get("/%2e%2e/not-in-repo", 400)
	_, wrong, err := h.deps.Auth.CreateToken(context.Background(), auth.CreateTokenInput{Name: "other", RegistryID: "other", Scopes: []auth.Permission{auth.PermissionPublish}})
	if err != nil {
		t.Fatal(err)
	}
	h.req("PUT", "/a.jar", "", map[string]string{"Authorization": "Bearer " + wrong}, 403)
	p := newFixture(t, "maven", registry.ModeProxy, "https://unused.invalid")
	p.write("PUT", "/a.jar", "never sent upstream", 405)
}

func TestCargoSparsePublishAndDownload(t *testing.T) {
	f := newFixture(t, "cargo", registry.ModeHosted, "")
	cfg := f.get("/config.json", 200)
	if !strings.Contains(cfg, "https://registry.test"+f.prefix+"/dl/") {
		t.Fatal(cfg)
	}
	var frame bytes.Buffer
	for _, s := range []string{`{"name":"test-kit","vers":"1.0.0","deps":[],"features":{}}`, "crate bytes"} {
		binary.Write(&frame, binary.LittleEndian, uint32(len(s)))
		frame.WriteString(s)
	}
	f.req("PUT", "/api/v1/crates/new", frame.String(), map[string]string{"Authorization": f.token}, 200)
	if got := f.get("/te/st/test-kit", 200); !strings.Contains(got, digest("crate bytes")) {
		t.Fatal(got)
	}
	if got := f.get("/dl/test-kit/1.0.0", 200); got != "crate bytes" {
		t.Fatal(got)
	}
	f.write("PUT", "/api/v1/crates/new", frame.String(), 409)
}

func TestOCIChunkedPushThenPull(t *testing.T) {
	f := newFixture(t, "oci", registry.ModeHosted, "")
	f.prefix = "" // Docker requires the root /v2/ discovery URL
	f.get("/v2/", 200)
	base := "/v2/acme/widget/"
	loc := f.write("POST", base+"blobs/uploads/", "", 202).Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil || u.Host != "registry.test" {
		t.Fatal(loc)
	}
	f.write("PATCH", u.Path, "part1", 202)
	d := "sha256:" + digest("part1part2")
	f.write("PUT", u.Path+"?digest="+d, "part2", 201)
	if f.get(base+"blobs/"+d, 200) != "part1part2" {
		t.Fatal("bad blob")
	}
	f.write("POST", base+"blobs/uploads/?digest=sha256:"+digest("other"), "bad", 400)
	index := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`
	f.write("PUT", base+"manifests/latest", index, 201)
	for _, ref := range []string{"latest", "sha256:" + digest(index)} {
		if f.get(base+"manifests/"+ref, 200) != index {
			t.Fatal(ref)
		}
	}
	if !strings.Contains(f.get(base+"tags/list", 200), "latest") {
		t.Fatal("missing tag")
	}
	f.mgr.DeleteInstance(context.Background(), f.inst.ID)
}

func TestGitLFSBasicTransfer(t *testing.T) {
	f := newFixture(t, "gitlfs", registry.ModeHosted, "")
	oid := digest("abc")
	p := "/objects/" + oid + "?size=3"
	body := `{"objects":[{"oid":"` + oid + `","size":3}],"operation":"upload"}`
	w := f.write("POST", "/objects/batch", body, 200)
	if !strings.Contains(w.Body.String(), f.prefix+p) {
		t.Fatal(w.Body.String())
	}
	f.write("PUT", p, "bad", 400)
	f.write("PUT", p, "abc", 201)
	w = f.req("POST", "/objects/batch", strings.Replace(body, "upload", "download", 1), nil, 200)
	if !strings.Contains(w.Body.String(), `"download"`) || f.get(p, 200) != "abc" {
		t.Fatal(w.Body.String())
	}
}

func TestConanRevisionsAndLegacy(t *testing.T) {
	f := newFixture(t, "conan", registry.ModeHosted, "")
	if v := f.req("GET", "/v1/ping", "", nil, 200).Header().Get("X-Conan-Server-Capabilities"); v != "revisions" {
		t.Fatal(v)
	}
	r := "/v2/conans/demo/1.0/_/_"
	prefix := r + "/revisions/abcd"
	for _, base := range []string{prefix, prefix + "/packages/abc/revisions/123"} {
		p := base + "/files/conanmanifest.txt"
		f.write("PUT", p, "manifest", 201)
		if !strings.Contains(f.get(base+"/files", 200), "conanmanifest.txt") || f.get(p, 200) != "manifest" {
			t.Fatal(p)
		}
	}
	if !strings.Contains(f.get(r+"/latest", 200), "abcd") || !strings.Contains(f.get(prefix+"/packages/abc/latest", 200), `"123"`) {
		t.Fatal("missing latest revisions")
	}
	v1 := "/v1/conans/lib/1.1/user/stable"
	loc := v1 + "/files/conanfile.py"
	if !strings.Contains(f.write("POST", v1+"/upload_urls", `{"conanfile.py":4}`, 200).Body.String(), loc) {
		t.Fatal("upload url")
	}
	f.write("PUT", loc, "test", 201)
	if !strings.Contains(f.get(v1+"/download_urls", 200), loc) || !strings.Contains(f.get(v1, 200), "098f6bcd4621d373cade4e832627b4f6") {
		t.Fatal("v1 download map or MD5 snapshot")
	}
}

func TestHubSnapshot(t *testing.T) {
	f := newFixture(t, "huggingface", registry.ModeHosted, "")
	sha := strings.Repeat("c", 40)
	for _, kind := range []string{"models", "datasets", "spaces"} {
		base := "/acme/demo"
		if kind != "models" {
			base = "/" + kind + base
		}
		f.write("PUT", base+"/resolve/"+sha+"/dir/config.json", "{\"n\":1}", 201)
		f.write("PUT", base+"/refs/main", sha, 201)
		w := f.req("HEAD", base+"/resolve/main/dir/config.json", "", nil, 200)
		if w.Header().Get("X-Repo-Commit") != sha || w.Header().Get("ETag") == "" || w.Header().Get("Content-Length") != "7" {
			t.Fatal(w.Header())
		}
		if !strings.Contains(f.get("/api/"+kind+"/acme/demo/revision/main", 200), `"rfilename":"dir/config.json"`) {
			t.Fatal("no snapshot siblings")
		}
	}
}

func TestNuGetPushAndRestore(t *testing.T) {
	f := newFixture(t, "nuget", registry.ModeHosted, "")
	if !strings.Contains(f.get("/v3/index.json", 200), f.prefix+"/push") {
		t.Fatal("no publish endpoint")
	}
	spec := `<package><metadata><id>Acme.Lib</id><version>1.0</version></metadata></package>`
	pkg := zipData(t, "Acme.Lib.nuspec", spec)
	f.req("PUT", "/push", pkg, map[string]string{"X-NuGet-ApiKey": f.token}, 201)
	if !strings.Contains(f.get("/flat/acme.lib/index.json", 200), `"1.0.0"`) {
		t.Fatal("missing version")
	}
	if f.get("/flat/acme.lib/1.0.0/acme.lib.1.0.0.nupkg", 200) != pkg || f.get("/flat/acme.lib/1.0.0/acme.lib.nuspec", 200) != spec {
		t.Fatal("bad flat-container data")
	}
}

func TestGoHostedImport(t *testing.T) {
	f := newFixture(t, goproxy.RegistryType, registry.ModeHosted, "")
	p := "/example.com/acme/@v/"
	mod := "module example.com/acme\n"
	pkg := zipData(t, "example.com/acme@v1.0.0/go.mod", mod)
	f.write("PUT", p+"v1.0.0.zip", "invalid zip", 400)
	f.write("PUT", p+"v1.0.0.zip", pkg, 201)
	f.write("PUT", p+"v1.0.0.zip", pkg, 409)
	if f.get(p+"list", 200) != "v1.0.0\n" || f.get(p+"v1.0.0.mod", 200) != mod || f.get(p+"v1.0.0.zip", 200) != pkg {
		t.Fatal("module data mismatch")
	}
	var info struct{ Version string }
	if json.Unmarshal([]byte(f.get(p+"v1.0.0.info", 200)), &info) != nil || info.Version != "v1.0.0" {
		t.Fatal(info)
	}
}
