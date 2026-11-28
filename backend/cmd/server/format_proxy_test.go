package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/amkargar/depyard/backend/internal/registry"
)

func TestSparseProxyUsesDownloadTemplate(t *testing.T) {
	var crates atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index/config.json":
			io.WriteString(w, `{"dl":"http://`+r.Host+`/archive/{lowerprefix}/{crate}-{version}.crate"}`)
		case "/archive/3/f/foo-1.1.0.crate":
			crates.Add(1)
			io.WriteString(w, "crate")
		case "/index/3/f/foo":
			io.WriteString(w, `{"vers":"1.1.0"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	f := newFixture(t, "cargo", registry.ModeProxy, up.URL+"/index")
	if !strings.Contains(f.get("/3/f/foo", 200), "1.1.0") || f.get("/dl/foo/1.1.0", 200) != "crate" || f.get("/dl/foo/1.1.0", 200) != "crate" || crates.Load() != 1 {
		t.Fatal("sparse discovery or download caching failed")
	}
}

func TestLinkedIndexes(t *testing.T) {
	for _, kind := range []string{"helm", "composer"} {
		t.Run(kind, func(t *testing.T) {
			path := "/packages.json"
			if kind == "helm" {
				path = "/index.yaml"
			}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				base := "http://" + r.Host + "/feed"
				switch r.URL.Path {
				case "/feed/packages.json":
					io.WriteString(w, `{"metadata-url":"`+base+`/p2/%package%.json","packages":{"acme/pkg":{"1":{"dist":{"url":"`+base+`/data/pkg.zip"}}}}}`)
				case "/feed/index.yaml":
					io.WriteString(w, "apiVersion: v1\nentries:\n  pkg:\n    - name: pkg\n      version: 1.0.0\n      urls: ["+base+"/data/pkg.zip]\n")
				case "/feed/data/pkg.zip":
					io.WriteString(w, "pkg")
				default:
					http.NotFound(w, r)
				}
			}))
			defer up.Close()
			f := newFixture(t, kind, registry.ModeHybrid, up.URL+"/feed")
			s := f.get(path, 200)
			if strings.Contains(s, up.URL) || !strings.Contains(s, f.prefix+"/data/pkg.zip") || f.get("/data/pkg.zip", 200) != "pkg" {
				t.Fatal(s)
			}
			if kind == "composer" && !strings.Contains(s, f.prefix+"/p2/%package%.json") {
				t.Fatal(s)
			}
			hosted := "apiVersion: v1\nentries: {}\n"
			if kind == "composer" {
				hosted = `{"packages":{}}`
			}
			f.write("PUT", path, hosted, 201)
			f.deps.Cache.PurgeRegistry(context.Background(), f.inst.ID)
			if got := strings.TrimSpace(f.get(path, 200)); got != strings.TrimSpace(hosted) {
				t.Fatal("local index was not preserved:", got)
			}
		})
	}
}

func TestConanProxyRevisionsAndLegacyDownloads(t *testing.T) {
	var loads atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/download_urls"):
			io.WriteString(w, `{"conanfile.py":"/archive/recipe"}`)
		case r.URL.Path == "/archive/recipe" || strings.HasSuffix(r.URL.Path, "/files/conanfile.py"):
			loads.Add(1)
			io.WriteString(w, "recipe")
		case strings.HasSuffix(r.URL.Path, "/latest"):
			io.WriteString(w, `{"revision":"abc"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	f := newFixture(t, "conan", registry.ModeProxy, up.URL)
	v1 := "/v1/conans/lib/1.0/_/_"
	v2 := strings.Replace(v1, "v1", "v2", 1)
	if !strings.Contains(f.get(v1+"/download_urls", 200), f.prefix+v1+"/files/conanfile.py") || !strings.Contains(f.get(v2+"/latest", 200), "abc") {
		t.Fatal("discovery failed")
	}
	for _, p := range []string{v1 + "/files/conanfile.py", v2 + "/revisions/abc/files/conanfile.py"} {
		if f.get(p, 200) != "recipe" || f.get(p, 200) != "recipe" {
			t.Fatal(p)
		}
	}
	if loads.Load() != 2 {
		t.Fatalf("%d upstream file downloads", loads.Load())
	}
}

func TestOCIProxyNegotiatesBearerAndVerifiesBlob(t *testing.T) {
	blob := "layer"
	d := digest(blob)
	var downloads atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tokens" {
			if r.URL.Query().Get("scope") != "repository:library/alpine:pull" {
				t.Error(r.URL.RawQuery)
			}
			io.WriteString(w, `{"token":"test-pull"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-pull" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/tokens",service="test"`)
			w.WriteHeader(401)
			return
		}
		downloads.Add(1)
		io.WriteString(w, blob)
	}))
	defer up.Close()
	f := newFixture(t, "oci", registry.ModeProxy, up.URL)
	p := "/v2/library/alpine/blobs/sha256:" + d
	if f.get(p, 200) != blob || f.get(p, 200) != blob || downloads.Load() != 1 {
		t.Fatal("blob cache")
	}
	f.get("/v2/library/alpine/blobs/sha256:"+digest("wrong"), 502)
}

func TestLFSProxyResolvesBasicAction(t *testing.T) {
	var downloads atomic.Int32
	oid := digest("bits")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/git/info/lfs/objects/batch":
			if r.Method != "POST" {
				t.Errorf("batch: %s", r.Method)
			}
			io.WriteString(w, `{"objects":[{"oid":"`+oid+`","size":4,"actions":{"download":{"href":"http://`+r.Host+`/signed","header":{"X-LFS-Auth":"action"}}}}]}`)
		case "/signed":
			if r.Header.Get("X-LFS-Auth") != "action" {
				t.Error("missing action header")
			}
			downloads.Add(1)
			io.WriteString(w, "bits")
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	f := newFixture(t, "gitlfs", registry.ModeProxy, up.URL+"/git/info/lfs")
	b := f.req("POST", "/objects/batch", `{"operation":"download","objects":[{"size":4,"oid":"`+oid+`"}]}`, nil, 200).Body.String()
	if strings.Contains(b, "X-LFS-Auth") || !strings.Contains(b, f.prefix+"/objects/") {
		t.Fatal(b)
	}
	for i := 0; i < 2; i++ {
		if f.get("/objects/"+oid+"?size=4", 200) != "bits" {
			t.Fatal("blob")
		}
	}
	if downloads.Load() != 1 {
		t.Fatal(downloads.Load())
	}
}

func TestHubProxyKeepsRevisionAcrossCDNRedirect(t *testing.T) {
	sha := strings.Repeat("b", 40)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Private-Key") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credential reached CDN")
		}
		w.Header().Set("ETag", `"hash"`)
		w.Header().Set("Content-Length", "5")
		if r.Method != "HEAD" {
			io.WriteString(w, "model")
		}
	}))
	defer cdn.Close()
	var gets atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Private-Key") != "secret" {
			t.Error("missing upstream config")
		}
		if r.Method == "GET" {
			gets.Add(1)
		}
		w.Header().Set("X-Repo-Commit", sha)
		http.Redirect(w, r, cdn.URL+"/asset", 302)
	}))
	defer up.Close()
	f := newFixture(t, "huggingface", registry.ModeProxy, up.URL)
	patch := f.inst.Clone()
	patch.UpstreamConfig = &registry.UpstreamConfig{AuthType: "header", AuthHeader: "X-Private-Key", AuthToken: "secret"}
	if _, err := f.mgr.UpdateInstance(context.Background(), f.inst.ID, patch); err != nil {
		t.Fatal(err)
	}
	p := "/acme/demo/resolve/" + sha + "/model"
	w := f.req("HEAD", p, "", nil, 200)
	if w.Header().Get("X-Repo-Commit") != sha || gets.Load() != 0 {
		t.Fatal("cold HEAD fetched payload or lost commit")
	}
	if f.get(p, 200) != "model" || f.get(p, 200) != "model" || gets.Load() != 1 {
		t.Fatal("resolve cache")
	}
}

func TestHubProxyTreePagination(t *testing.T) {
	const tree = "/api/models/acme/demo/tree/main"
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != tree || r.URL.Query().Get("recursive") != "true" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("cursor") == "p2" {
			io.WriteString(w, `[{"type":"file","path":"b.bin"}]`)
			return
		}
		w.Header().Set("Link", `<http://`+r.Host+tree+`?recursive=true&cursor=p2>; rel="next"`)
		io.WriteString(w, `[{"type":"file","path":"a.bin","xetHash":"external-xet-service"}]`)
	}))
	defer up.Close()
	f := newFixture(t, "huggingface", registry.ModeProxy, up.URL)
	for i := 0; i < 2; i++ {
		w := f.req("GET", tree+"?recursive=true", "", nil, 200)
		p2 := tree + "?cursor=p2&recursive=true"
		if got := w.Header().Get("Link"); got != `<https://registry.test`+f.prefix+p2+`>; rel="next"` {
			t.Fatalf("continuation escaped instance or lost cursor: %s", got)
		}
		if got := w.Body.String(); !strings.Contains(got, "a.bin") || strings.Contains(got, "xetHash") {
			t.Fatal(got)
		}
		if got := f.get(p2, 200); !strings.Contains(got, "b.bin") {
			t.Fatal(got)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, expected one per page", got)
	}
}

func TestNuGetProxyDiscoversFlatContainer(t *testing.T) {
	var gets atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/service.json":
			io.WriteString(w, `{"resources":[{"@type":"PackageBaseAddress/3.0.0","@id":"http://`+r.Host+`/content/"}]}`)
		case "/content/foo/index.json":
			io.WriteString(w, `{"versions":["1.0.0"]}`)
		case "/content/foo/1.0.0/foo.1.0.0.nupkg":
			gets.Add(1)
			io.WriteString(w, "nupkg")
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	f := newFixture(t, "nuget", registry.ModeProxy, up.URL+"/v3/service.json")
	var index struct{ Resources []map[string]string }
	if json.Unmarshal([]byte(f.get("/index.json", 200)), &index) != nil || len(index.Resources) != 1 || !strings.Contains(index.Resources[0]["@id"], f.prefix+"/flat/") {
		t.Fatal(index)
	}
	if !strings.Contains(f.get("/flat/foo/index.json", 200), "1.0.0") {
		t.Fatal("versions")
	}
	for i := 0; i < 2; i++ {
		if f.get("/flat/foo/1.0.0/foo.1.0.0.nupkg", 200) != "nupkg" {
			t.Fatal("package")
		}
	}
	if gets.Load() != 1 {
		t.Fatal(gets.Load())
	}
}

func TestProxyMissesRefreshAndDoNotPoisonCache(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, n)
	}))
	defer up.Close()
	f := newFixture(t, "maven", registry.ModeProxy, up.URL)
	p := "/acme/maven-metadata.xml"
	f.get(p, 404)
	if f.get(p, 200) != "2" || f.get(p, 200) != "2" {
		t.Fatal("bad cache after miss")
	}
	patch := f.inst.Clone()
	patch.CachePolicy.MetadataTTLSeconds = 0
	if _, err := f.mgr.UpdateInstance(context.Background(), patch.ID, patch); err != nil {
		t.Fatal(err)
	}
	if f.get(p, 200) != "3" {
		t.Fatal("mutable index never refreshed")
	}
	stats, err := f.deps.Storage.GetGlobalStats(context.Background())
	if err != nil || stats.PhysicalBlobCount != 1 {
		t.Fatal(stats, err)
	}
}

func TestDedicatedOCIContinuationStaysOnHost(t *testing.T) {
	f := newFixture(t, "oci", registry.ModeHosted, "")
	inst := f.inst.Clone()
	inst.Hostname = "images.example.test"
	if _, err := f.mgr.UpdateInstance(context.Background(), inst.ID, inst); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "https://images.example.test/v2/acme/blobs/uploads/", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	if w.Code != 202 || !strings.HasPrefix(w.Header().Get("Location"), "https://images.example.test/v2/") {
		t.Fatal(w.Code, w.Header())
	}
	if err := f.mgr.DeleteInstance(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
}
