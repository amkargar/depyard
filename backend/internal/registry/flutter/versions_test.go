package flutter_test

import (
	"encoding/json"
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
	"github.com/amkargar/depyard/backend/internal/registry/flutter"
	"github.com/amkargar/depyard/backend/internal/storage"
	memstore "github.com/amkargar/depyard/backend/internal/store/memory"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

func TestParseConfigDownloadStorageSettings(t *testing.T) {
	cfg, err := flutter.ParseConfig(nil)
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	if cfg.StorageName == "" {
		t.Fatal("expected a default storage name")
	}
	if len(cfg.DownloadStorage) != len(flutter.DefaultAllowedPrefixes) {
		t.Fatalf("expected %d default storage categories, got %d", len(flutter.DefaultAllowedPrefixes), len(cfg.DownloadStorage))
	}
	for _, d := range cfg.DownloadStorage {
		if d.UpstreamURL != "" || (d.Enabled != nil && !*d.Enabled) {
			t.Fatalf("expected default category to inherit upstream and be enabled: %+v", d)
		}
	}

	cfg, err = flutter.ParseConfig(map[string]any{
		"storage_name":  " Regional Flutter Mirror ",
		"download_storage": []map[string]any{
			{
				"name":          "flutter_infra_release",
				"upstream_url":  "https://storage.flutter-io.cn",
				"manifest_ttl_seconds": 120,
			},
			{
				"name":    "download.flutter.io",
				"enabled": false,
			},
		},
	})
	if err != nil {
		t.Fatalf("parse regional config: %v", err)
	}
	if cfg.StorageName != "Regional Flutter Mirror" {
		t.Fatalf("unexpected storage name %q", cfg.StorageName)
	}

	var infra *flutter.DownloadStorageSetting
	var gradle *flutter.DownloadStorageSetting
	for i := range cfg.DownloadStorage {
		switch cfg.DownloadStorage[i].Name {
		case "flutter_infra_release":
			infra = &cfg.DownloadStorage[i]
		case "download.flutter.io":
			gradle = &cfg.DownloadStorage[i]
		}
	}
	if infra == nil || infra.UpstreamURL != "https://storage.flutter-io.cn" || infra.ManifestTTLSeconds != 120 {
		t.Fatalf("unexpected flutter_infra_release category: %+v", infra)
	}
	if gradle == nil || gradle.Enabled == nil || *gradle.Enabled {
		t.Fatalf("expected download.flutter.io to be disabled: %+v", gradle)
	}
	// Canonical categories that were omitted are still appended for client compatibility.
	if len(cfg.DownloadStorage) != len(flutter.DefaultAllowedPrefixes) {
		t.Fatalf("expected all canonical categories to remain present, got %d", len(cfg.DownloadStorage))
	}

	for _, bad := range []map[string]any{
		{"download_storage": []map[string]any{{"name": ""}}},
		{"download_storage": []map[string]any{{"name": "a"}, {"name": "a"}}},
		{"download_storage": []map[string]any{{"name": "flutter_infra_release", "upstream_url": "ftp://nope"}}},
		{"download_storage": []map[string]any{{"name": "flutter_infra_release", "manifest_ttl_seconds": -1}}},
		{"releases_base_url": "ftp://example.com"},
		{"registry_ttl_seconds": -10},
		{"storage_name": 12},
	} {
		if _, err := flutter.ParseConfig(bad); err == nil {
			t.Fatalf("expected invalid config to be rejected: %#v", bad)
		}
	}
}

func TestDownloadStorageCategoryRoutingAndVersionRegistry(t *testing.T) {
	var infraHits, gradleHits, releasesHits int
	infraUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		infraHits++
		if r.URL.Path != "/flutter_infra_release/releases/releases_linux.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_url": "https://cdn.example.internal/flutter_infra_release/releases",
			"current_release": {"stable": "3.27.1", "beta": "3.28.0-0.1.pre", "dev": ""},
			"releases": [
				{"hash": "aaaa", "channel": "stable", "version": "3.27.1", "dart_sdk_version": "3.6.0", "release_date": "2024-12-12", "archive": "stable/linux/flutter_linux_3.27.1-stable.tar.xz"},
				{"hash": "bbbb", "channel": "stable", "version": "3.27.0", "dart_sdk_version": "3.6.0", "release_date": "2024-12-05", "archive": "stable/linux/flutter_linux_3.27.0-stable.tar.xz"}
			]
		}`))
	}))
	defer infraUpstream.Close()

	gradleUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gradleHits++
		if r.URL.Path != "/download.flutter.io/io/flutter/flutter_embedding_debug/1.0.0/flutter_embedding_debug-1.0.0.pom" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("<project/>"))
	}))
	defer gradleUpstream.Close()

	releasesUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releasesHits++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/releases_linux.json":
			_, _ = w.Write([]byte(`{
				"base_url": "https://cdn.example.internal/flutter_infra_release/releases",
				"current_release": {"stable": "3.27.1"},
				"releases": [
					{"hash": "aaaa", "channel": "stable", "version": "3.27.1", "dart_sdk_version": "3.6.0", "release_date": "2024-12-12", "archive": "stable/linux/flutter_linux_3.27.1-stable.tar.xz"},
					{"hash": "cccc", "channel": "beta", "version": "3.28.0-0.1.pre", "dart_sdk_version": "3.7.0", "archive": "beta/linux/flutter_linux_3.28.0-0.1.pre-beta.tar.xz"}
				]
			}`))
		case "/releases_macos.json":
			_, _ = w.Write([]byte(`{
				"base_url": "https://cdn.example.internal/flutter_infra_release/releases",
				"current_release": {"stable": "3.27.1"},
				"releases": [
					{"hash": "aaaa", "channel": "stable", "version": "3.27.1", "dart_sdk_version": "3.6.0", "archive": "stable/macos/flutter_macos_3.27.1-stable.zip"},
					{"hash": "dddd", "channel": "stable", "version": "3.27.0", "dart_sdk_version": "3.6.0", "archive": "stable/macos/flutter_macos_3.27.0-stable.zip"}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer releasesUpstream.Close()

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
		ID:          "fl-versions",
		Name:        "Regional Flutter Mirror",
		Slug:        "flutter-cn",
		Type:        flutter.RegistryType,
		Enabled:     true,
		Mode:        registry.ModeProxy,
		UpstreamURL: infraUpstream.URL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
		Config: map[string]any{
			"storage_name":      "China Flutter Storage",
			"registry_ttl_seconds": 60,
			"releases_base_url": releasesUpstream.URL,
			"download_storage": []map[string]any{
				{"name": "flutter_infra_release", "upstream_url": infraUpstream.URL},
				{"name": "download.flutter.io", "upstream_url": gradleUpstream.URL},
			},
		},
	}

	flReg, err := flutter.New(inst, deps, flutter.NewMemoryStore())
	if err != nil {
		t.Fatalf("flutter.New: %v", err)
	}

	doReq := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(registry.WithBaseURL(req.Context(), "https://mirror.example.com/r/flutter-cn"))
		rec := httptest.NewRecorder()
		flReg.Handler().ServeHTTP(rec, req)
		return rec
	}

	// 1. flutter_infra_release requests must be routed to the category upstream, not the registry upstream.
	manifestRec := doReq("/flutter_infra_release/releases/releases_linux.json")
	if manifestRec.Code != http.StatusOK {
		t.Fatalf("expected manifest fetch to succeed, got %d: %s", manifestRec.Code, manifestRec.Body.String())
	}
	if infraHits != 1 {
		t.Fatalf("expected 1 flutter_infra_release upstream hit, got %d", infraHits)
	}
	var manifestPayload map[string]any
	if err := json.Unmarshal(manifestRec.Body.Bytes(), &manifestPayload); err != nil {
		t.Fatalf("decode rewritten manifest: %v", err)
	}
	wantBase := "https://mirror.example.com/r/flutter-cn/flutter_infra_release/releases"
	if manifestPayload["base_url"] != wantBase {
		t.Fatalf("expected base_url rewrite %q, got %v", wantBase, manifestPayload["base_url"])
	}

	// 2. Gradle maven requests route to their own configured category upstream.
	mavenRec := doReq("/download.flutter.io/io/flutter/flutter_embedding_debug/1.0.0/flutter_embedding_debug-1.0.0.pom")
	if mavenRec.Code != http.StatusOK {
		t.Fatalf("expected maven fetch to succeed, got %d: %s", mavenRec.Code, mavenRec.Body.String())
	}
	if gradleHits != 1 {
		t.Fatalf("expected 1 download.flutter.io upstream hit, got %d", gradleHits)
	}

	// 3. Version registry endpoint exposes every known SDK version with storage settings.
	regRec := doReq("/api/registry")
	if regRec.Code != http.StatusOK {
		t.Fatalf("version registry: status %d body %s", regRec.Code, regRec.Body.String())
	}
	var snapshot flutter.VersionRegistry
	if err := json.Unmarshal(regRec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode version registry: %v", err)
	}
	if snapshot.StorageName != "China Flutter Storage" {
		t.Fatalf("unexpected storage name %q", snapshot.StorageName)
	}
	if snapshot.TotalVersions != 3 {
		t.Fatalf("expected 3 distinct Flutter versions across linux+macos manifests, got %d (%+v)", snapshot.TotalVersions, snapshot.Versions)
	}
	if snapshot.Versions[0].Version != "3.28.0-0.1.pre" {
		t.Fatalf("expected newest (beta pre-release) version first, got %q", snapshot.Versions[0].Version)
	}
	if snapshot.CurrentRelease != "3.27.1" {
		t.Fatalf("expected stable current release 3.27.1, got %q", snapshot.CurrentRelease)
	}

	byVersion := map[string]flutter.FlutterVersion{}
	for _, v := range snapshot.Versions {
		byVersion[v.Version] = v
	}
	v3271 := byVersion["3.27.1"]
	if v3271.DartSDKVersion != "3.6.0" {
		t.Fatalf("expected dart sdk version 3.6.0, got %q", v3271.DartSDKVersion)
	}
	if len(v3271.Downloads) != 2 {
		t.Fatalf("expected linux + macos downloads for 3.27.1, got %+v", v3271.Downloads)
	}
	if !strings.HasPrefix(v3271.SDKDownloadURL, "https://mirror.example.com/r/flutter-cn/flutter_infra_release/releases/") {
		t.Fatalf("unexpected SDK download url %q", v3271.SDKDownloadURL)
	}
	if len(v3271.Storage) == 0 || v3271.Storage[0].Category != "flutter_infra_release" {
		t.Fatalf("expected storage targets for flutter_infra_release, got %+v", v3271.Storage)
	}
	for _, st := range v3271.Storage {
		if st.Category == "flutter_infra_release" && st.UpstreamURL != infraUpstream.URL {
			t.Fatalf("expected category upstream to be the override, got %q", st.UpstreamURL)
		}
		if st.Category == "dart-archive" && st.InheritedFrom != "registry" {
			t.Fatalf("expected dart-archive to inherit the registry upstream, got %+v", st)
		}
	}

	// 4. Per-version endpoint.
	entryRec := doReq("/api/registry/versions/3.27.0")
	if entryRec.Code != http.StatusOK || !strings.Contains(entryRec.Body.String(), "3.27.0") {
		t.Fatalf("unexpected version entry response %d: %s", entryRec.Code, entryRec.Body.String())
	}
	missingRec := doReq("/api/registry/versions/9.9.9")
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown version, got %d", missingRec.Code)
	}

	// 5. Disabling a category must genuinely stop serving it (403, not upstream 404).
	disabledInst := inst.Clone()
	disabledInst.Config = map[string]any{
		"download_storage": []map[string]any{
			{"name": "flutter_infra_release", "upstream_url": infraUpstream.URL},
			{"name": "dart-archive", "enabled": false},
		},
	}
	disabledReg, err := flutter.New(disabledInst, deps, flutter.NewMemoryStore())
	if err != nil {
		t.Fatalf("flutter.New (disabled): %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/dart-archive/channels/stable/release/latest/release-linux-x64.json", nil)
	req = req.WithContext(registry.WithBaseURL(req.Context(), "https://mirror.example.com/r/flutter-cn"))
	rec := httptest.NewRecorder()
	disabledReg.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("expected disabled category to return 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if disabledReg.MatchesCompatPath("/dart-archive/channels/stable/release/latest/release-linux-x64.json") {
		t.Fatal("disabled category must not claim root compatibility routes")
	}

	// 6. Artifact explorer API now advertises per-category storage settings.
	artRec := doReq("/api/artifacts")
	if artRec.Code != http.StatusOK || !strings.Contains(artRec.Body.String(), "download_storage") {
		t.Fatalf("expected explorer API to expose download_storage, got %d: %s", artRec.Code, artRec.Body.String())
	}
}

func TestVersionRegistryUnavailableUpstream(t *testing.T) {
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer downstream.Close()

	ms := memstore.New()
	fs, err := storage.NewFSBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("fs store: %v", err)
	}
	storageSvc := storage.NewService(fs, ms)
	deps := registry.ServiceDeps{
		Storage:  storageSvc,
		Cache:    cache.NewCoordinator(storageSvc),
		Upstream: upstream.NewClient(2 * time.Second),
		Auth:     auth.NewService(ms, time.Hour),
		Metrics:  metrics.NewCollector(ms),
		Audit:    audit.NewService(ms),
	}
	inst := &registry.Instance{
		ID:          "fl-down",
		Name:        "Down",
		Slug:        "fl-down",
		Type:        flutter.RegistryType,
		Enabled:     true,
		Mode:        registry.ModeProxy,
		UpstreamURL: downstream.URL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
	}
	flReg, err := flutter.New(inst, deps, flutter.NewMemoryStore())
	if err != nil {
		t.Fatalf("flutter.New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/registry", nil)
	req = req.WithContext(registry.WithBaseURL(req.Context(), "https://mirror.example.com/r/fl-down"))
	rec := httptest.NewRecorder()
	flReg.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with warnings when upstream manifests are unreachable, got %d: %s", rec.Code, rec.Body.String())
	}
	var snapshot flutter.VersionRegistry
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode empty version registry: %v", err)
	}
	if snapshot.TotalVersions != 0 {
		t.Fatalf("expected no versions when upstream is down, got %d", snapshot.TotalVersions)
	}
	if len(snapshot.Warnings) == 0 {
		t.Fatal("expected warnings describing the upstream failure")
	}
}
