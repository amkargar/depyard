package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amkargar/mirror-registry/backend/internal/admin"
	"github.com/amkargar/mirror-registry/backend/internal/audit"
	"github.com/amkargar/mirror-registry/backend/internal/auth"
	"github.com/amkargar/mirror-registry/backend/internal/cache"
	"github.com/amkargar/mirror-registry/backend/internal/metrics"
	"github.com/amkargar/mirror-registry/backend/internal/registry"
	"github.com/amkargar/mirror-registry/backend/internal/registry/flutter"
	"github.com/amkargar/mirror-registry/backend/internal/server"
	"github.com/amkargar/mirror-registry/backend/internal/storage"
	memstore "github.com/amkargar/mirror-registry/backend/internal/store/memory"
	"github.com/amkargar/mirror-registry/backend/internal/upstream"
)

// baseSettingsHarness wires a full server with the live base-config provider, mirroring main.go.
type baseSettingsHarness struct {
	ts        *httptest.Server
	token     string
	mgr       *registry.Manager
	handler   *admin.Handler
	seenHost  *atomic.Value
	primary   *registry.Instance
	secondary *registry.Instance
}

func newBaseSettingsHarness(t *testing.T, upstreamFor func(host string) string) *baseSettingsHarness {
	t.Helper()
	ctx := context.Background()

	seenHost := &atomic.Value{}
	seenHost.Store("")

	// A single mock upstream whose response records which host was dialled.
	var mock *httptest.Server
	mock = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost.Store(r.Host)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"base_url":        "http://" + r.Host + "/flutter_infra_release/releases",
			"current_release": map[string]any{"stable": "3.27.4"},
			"releases": []map[string]any{
				{"version": "3.27.4", "channel": "stable", "archive": "stable/linux/flutter_linux_3.27.4-stable.tar.xz", "release_date": "2025-01-10"},
			},
		})
	}))
	t.Cleanup(mock.Close)

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

	_, _ = authSvc.EnsureBootstrapAdmin(ctx, "admin", "admin@test.local", "adminpass")
	_, adminToken, _, err := authSvc.Login(ctx, "admin", "adminpass", "test", "127.0.0.1")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}

	liveDefaults := &admin.LiveDefaults{}
	deps := registry.ServiceDeps{
		Storage:  storageSvc,
		Cache:    cacheCoord,
		Upstream: upClient,
		Auth:     authSvc,
		Metrics:  metricsCol,
		Audit:    auditSvc,
		Defaults: liveDefaults,
	}
	mgr := registry.NewManager(ms, deps)
	if err := mgr.RegisterType(flutter.Registration(flutter.NewMemoryStore())); err != nil {
		t.Fatalf("register flutter: %v", err)
	}

	// Primary registry: explicitly configured TTL so registry values must win over the base.
	primary, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name:        "Flutter Primary",
		Slug:        "flutter-primary",
		Type:        flutter.RegistryType,
		Enabled:     true,
		Mode:        registry.ModeProxy,
		UpstreamURL: mock.URL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
		CachePolicy: registry.DefaultCachePolicy(),
		Config: map[string]any{
			"manifest_ttl_seconds": 42,
		},
		IsDefault: true,
	})
	if err != nil {
		t.Fatalf("create primary: %v", err)
	}
	// Secondary registry: bare config so it inherits everything from the base.
	secondary, err := mgr.CreateInstance(ctx, &registry.Instance{
		Name:        "Flutter Secondary",
		Slug:        "flutter-secondary",
		Type:        flutter.RegistryType,
		Enabled:     true,
		Mode:        registry.ModeProxy,
		UpstreamURL: mock.URL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
		CachePolicy: registry.DefaultCachePolicy(),
	})
	if err != nil {
		t.Fatalf("create secondary: %v", err)
	}

	settings := admin.DefaultSettings("", false)
	handler := admin.NewHandler(mgr, authSvc, storageSvc, cacheCoord, metricsCol, auditSvc, ms, settings)
	handler.SetUpstreamClient(upClient)
	liveDefaults.Set(func(ctx context.Context, registryType string) (map[string]any, error) {
		st, err := handler.GetSettings(ctx)
		if err != nil || st == nil {
			return nil, err
		}
		return st.BaseConfig(ctx, registryType)
	})

	srv := server.New(server.Options{
		Manager:      mgr,
		AdminHandler: handler,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	_ = upstreamFor // retained for readability at call sites
	return &baseSettingsHarness{ts: ts, token: adminToken, mgr: mgr, handler: handler, seenHost: seenHost, primary: primary, secondary: secondary}
}

func (h *baseSettingsHarness) putSettings(t *testing.T, payload map[string]any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPut, h.ts.URL+"/api/admin/settings", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT settings: %v", err)
	}
	return resp
}

func (h *baseSettingsHarness) get(t *testing.T, path string, auth bool) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.ts.URL+path, nil)
	if auth {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}

func TestFlutterBaseSettingsApplyAndInheritance(t *testing.T) {
	h := newBaseSettingsHarness(t, nil)

	// --- 1. Base settings round-trip through PUT/GET -------------------------------
	basePayload := map[string]any{
		"site_title": "Base Mirror",
		"default_flutter_storage_config": map[string]any{
			"upstream_url":         h.ts.URL, // unused upstream root for this test
			"manifest_ttl_seconds": 77,
			"storage_name":         "Base Storage",
			"download_storage": []any{
				map[string]any{"name": "dart-archive", "enabled": false},
			},
		},
		"flutter_base_config_mode": "defaults",
	}
	resp := h.putSettings(t, basePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 saving base settings, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	var got admin.PlatformSettings
	decodeBody(t, h.get(t, "/api/admin/settings", true), &got)
	if got.DefaultFlutterStorageConfig["manifest_ttl_seconds"] != float64(77) {
		t.Fatalf("expected base manifest TTL 77, got %v", got.DefaultFlutterStorageConfig)
	}
	if got.FlutterBaseConfigMode != admin.FlutterBaseModeDefaults {
		t.Fatalf("expected defaults mode, got %q", got.FlutterBaseConfigMode)
	}

	// --- 2. Invalid base config is rejected with the parser's error ---------------
	badResp := h.putSettings(t, map[string]any{
		"default_flutter_storage_config": map[string]any{"not_a_key": true},
	})
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown base key, got %d", badResp.StatusCode)
	}
	badResp.Body.Close()

	badURL := h.putSettings(t, map[string]any{
		"default_flutter_storage_config": map[string]any{"upstream_url": "ftp://nope"},
	})
	if badURL.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid base upstream, got %d", badURL.StatusCode)
	}
	badURL.Body.Close()

	// Re-apply the good payload; the rejected writes must not have leaked.
	resp = h.putSettings(t, basePayload)
	resp.Body.Close()

	// --- 3. Impact preview reports changes and writes nothing --------------------
	var impact admin.BaseConfigImpact
	decodeBody(t, h.get(t, "/api/admin/settings/base-config/flutter/impact", true), &impact)
	if impact.DryRun != true {
		t.Fatal("impact preview must be a dry run")
	}
	if len(impact.Registries) != 2 {
		t.Fatalf("expected 2 flutter registries in impact, got %d", len(impact.Registries))
	}
	var secondaryImpact admin.RegistryBaseImpact
	for _, r := range impact.Registries {
		if r.Slug == h.secondary.Slug {
			secondaryImpact = r
		}
	}
	if len(secondaryImpact.Changes) == 0 {
		t.Fatalf("expected pending changes for bare registry, got none")
	}
	// The primary registry already sets manifest_ttl_seconds, so fill-missing must not touch it.
	for _, r := range impact.Registries {
		if r.Slug != h.primary.Slug {
			continue
		}
		for _, c := range r.Changes {
			if c.Key == "manifest_ttl_seconds" {
				t.Fatalf("fill-missing must not overwrite a registry-provided field: %+v", c)
			}
		}
	}

	// --- 4. Apply with fill-missing ----------------------------------------------
	applyBody := map[string]any{"mode": "fill-missing"}
	body, _ := json.Marshal(applyBody)
	applyReq, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/admin/settings/base-config/flutter/apply", strings.NewReader(string(body)))
	applyReq.Header.Set("Authorization", "Bearer "+h.token)
	applyReq.Header.Set("Content-Type", "application/json")
	applyResp, err := h.ts.Client().Do(applyReq)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var applied admin.BaseConfigImpact
	decodeBody(t, applyResp, &applied)
	if applied.Applied < 1 {
		t.Fatalf("expected at least one registry updated, got %+v", applied)
	}
	if applied.Failed != 0 {
		t.Fatalf("expected no failures, got %+v", applied)
	}

	inst, err := h.mgr.GetInstance(context.Background(), h.secondary.ID)
	if err != nil {
		t.Fatalf("reload secondary: %v", err)
	}
	if inst.Config["manifest_ttl_seconds"] != float64(77) {
		t.Fatalf("expected applied base TTL on secondary, got %v", inst.Config)
	}

	// Repeat apply must be a no-op (idempotent).
	applyReq2, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/admin/settings/base-config/flutter/apply", strings.NewReader(`{"mode":"fill-missing"}`))
	applyReq2.Header.Set("Authorization", "Bearer "+h.token)
	applyResp2, err := h.ts.Client().Do(applyReq2)
	if err != nil {
		t.Fatalf("apply repeat: %v", err)
	}
	var applied2 admin.BaseConfigImpact
	decodeBody(t, applyResp2, &applied2)
	if applied2.Applied != 0 {
		t.Fatalf("expected idempotent apply, got %d registries changed", applied2.Applied)
	}

	// --- 5. Live inheritance: switching to "inherit" takes effect without a restart ----
	// Until now the primary registry keeps its own TTL (42); in inherit mode the base supplies only
	// fields the registry has not set, so 42 must survive while the base supplies the rest.
	basePayload["flutter_base_config_mode"] = "inherit"
	basePayload["default_flutter_storage_config"] = map[string]any{
		"manifest_ttl_seconds": 99,
		"storage_name":         "Inherited Storage",
	}
	resp = h.putSettings(t, basePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 switching to inherit, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The watcher polls every 3s; assert it converges without any instance write.
	deadline := time.Now().Add(15 * time.Second)
	var primaryManifest *http.Response
	for time.Now().Before(deadline) {
		primaryManifest = h.get(t, "/r/flutter-primary/flutter_infra_release/releases/releases_linux.json", false)
		cacheControl := primaryManifest.Header.Get("Cache-Control")
		primaryManifest.Body.Close()
		if strings.Contains(cacheControl, "max-age=42") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	// Registry-level value (42) must still win even in inherit mode.
	primaryManifest = h.get(t, "/r/flutter-primary/flutter_infra_release/releases/releases_linux.json", false)
	if cc := primaryManifest.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=42") {
		t.Fatalf("registry TTL must win over base in inherit mode, got %q", cc)
	}
	primaryManifest.Body.Close()

	// A fresh registry with no config at all should now serve with the inherited base TTL (99).
	fresh, err := h.mgr.CreateInstance(context.Background(), &registry.Instance{
		Name:        "Flutter Fresh",
		Slug:        "flutter-fresh",
		Type:        flutter.RegistryType,
		Enabled:     true,
		Mode:        registry.ModeProxy,
		UpstreamURL: h.secondary.UpstreamURL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
		CachePolicy: registry.DefaultCachePolicy(),
	})
	if err != nil {
		t.Fatalf("create fresh: %v", err)
	}
	freshManifest := h.get(t, "/r/"+fresh.Slug+"/flutter_infra_release/releases/releases_linux.json", false)
	if cc := freshManifest.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age=99") {
		t.Fatalf("expected inherited base TTL 99 on fresh registry, got %q", cc)
	}
	freshManifest.Body.Close()

	// In defaults mode the base must NOT leak into a registry that never set the field.
	basePayload["flutter_base_config_mode"] = "defaults"
	resp = h.putSettings(t, basePayload)
	resp.Body.Close()

	fresh2, err := h.mgr.CreateInstance(context.Background(), &registry.Instance{
		Name:        "Flutter Fresh Defaults",
		Slug:        "flutter-fresh-defaults",
		Type:        flutter.RegistryType,
		Enabled:     true,
		Mode:        registry.ModeProxy,
		UpstreamURL: h.secondary.UpstreamURL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy:  registry.AuthPolicy{AllowAnonymousRead: true},
		CachePolicy: registry.DefaultCachePolicy(),
	})
	if err != nil {
		t.Fatalf("create fresh2: %v", err)
	}
	// The watcher needs to re-read the switched mode; poll until the inherited TTL is gone.
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r := h.get(t, "/r/"+fresh2.Slug+"/flutter_infra_release/releases/releases_linux.json", false)
		cc := r.Header.Get("Cache-Control")
		r.Body.Close()
		if strings.Contains(cc, "max-age=300") {
			return // default TTL restored: inheritance correctly disabled
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("expected default TTL (300) after switching back to defaults mode")
}

// TestFlutterBaseSettingsApplyOverwrite verifies overwrite mode changes only the selected fields.
func TestFlutterBaseSettingsApplyOverwrite(t *testing.T) {
	h := newBaseSettingsHarness(t, nil)

	resp := h.putSettings(t, map[string]any{
		"default_flutter_storage_config": map[string]any{
			"manifest_ttl_seconds": 55,
			"storage_name":         "Overwritten",
		},
	})
	resp.Body.Close()

	applyReq, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/admin/settings/base-config/flutter/apply",
		strings.NewReader(`{"mode":"overwrite","fields":["manifest_ttl_seconds"],"registry_ids":["`+h.primary.ID+`"]}`))
	applyReq.Header.Set("Authorization", "Bearer "+h.token)
	applyReq.Header.Set("Content-Type", "application/json")
	applyResp, err := h.ts.Client().Do(applyReq)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var out admin.BaseConfigImpact
	decodeBody(t, applyResp, &out)
	if out.Applied != 1 {
		t.Fatalf("expected exactly one registry updated, got %+v", out)
	}
	if len(out.Registries) != 1 || out.Registries[0].ID != h.primary.ID {
		t.Fatalf("expected only the targeted registry, got %+v", out.Registries)
	}

	inst, err := h.mgr.GetInstance(context.Background(), h.primary.ID)
	if err != nil {
		t.Fatalf("reload primary: %v", err)
	}
	if inst.Config["manifest_ttl_seconds"] != float64(55) {
		t.Fatalf("expected overwritten TTL 55, got %v", inst.Config)
	}
	if _, ok := inst.Config["storage_name"]; ok {
		t.Fatalf("field filter must prevent storage_name from being written: %v", inst.Config)
	}

	// Unknown field in the filter is rejected.
	badReq, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/admin/settings/base-config/flutter/apply",
		strings.NewReader(`{"mode":"overwrite","fields":["nope"]}`))
	badReq.Header.Set("Authorization", "Bearer "+h.token)
	badResp, err := h.ts.Client().Do(badReq)
	if err != nil {
		t.Fatalf("apply bad field: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown apply field, got %d", badResp.StatusCode)
	}
}

// TestFlutterBaseSettingsDisabledCategoryEnforced verifies base-level category policy is honoured
// at request time for a registry that does not set its own download_storage.
func TestFlutterBaseSettingsDisabledCategoryEnforced(t *testing.T) {
	h := newBaseSettingsHarness(t, nil)

	resp := h.putSettings(t, map[string]any{
		"flutter_base_config_mode": "inherit",
		"default_flutter_storage_config": map[string]any{
			"download_storage": []any{
				map[string]any{"name": "dart-archive", "enabled": false},
			},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 saving base settings, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r := h.get(t, "/r/flutter-secondary/dart-archive/channels/stable/release/latest/release-linux-x64.json", false)
		body := map[string]any{}
		status := r.StatusCode
		_ = json.NewDecoder(r.Body).Decode(&body)
		r.Body.Close()
		if status == http.StatusForbidden {
			msg, _ := body["error"].(string)
			if !strings.Contains(msg, "disabled on this registry") {
				t.Fatalf("unexpected 403 message: %q", msg)
			}
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("expected 403 for a base-disabled category on an unconfigured registry")
}
