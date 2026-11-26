package flutter_test

import (
	"context"
	"testing"

	"github.com/amkargar/depyard/backend/internal/registry/flutter"
)

func TestNormalizeBaseConfig(t *testing.T) {
	out, err := flutter.NormalizeBaseConfig(nil)
	if err != nil {
		t.Fatalf("empty base config: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty base config, got %v", out)
	}

	out, err = flutter.NormalizeBaseConfig(map[string]any{
		"storage_name":         " Base Storage ",
		"upstream_url":         "https://storage.flutter-io.cn/",
		"manifest_ttl_seconds": 120,
	})
	if err != nil {
		t.Fatalf("valid base config: %v", err)
	}
	if out["storage_name"] != " Base Storage " {
		t.Fatalf("expected storage_name preserved, got %v", out["storage_name"])
	}
	// upstream_url is base-only and must survive normalisation.
	if out["upstream_url"] != "https://storage.flutter-io.cn/" {
		t.Fatalf("expected upstream_url preserved, got %v", out["upstream_url"])
	}

	if _, err := flutter.NormalizeBaseConfig(map[string]any{"bogus": true}); err == nil {
		t.Fatal("expected unknown key rejection")
	}
	if _, err := flutter.NormalizeBaseConfig(map[string]any{"upstream_url": "ftp://nope"}); err == nil {
		t.Fatal("expected invalid upstream_url rejection")
	}
	if _, err := flutter.NormalizeBaseConfig(map[string]any{"manifest_ttl_seconds": -1}); err == nil {
		t.Fatal("expected out-of-range TTL rejection")
	}
	if _, err := flutter.NormalizeBaseConfig(map[string]any{"download_storage_mode": "explode"}); err == nil {
		t.Fatal("expected invalid download_storage_mode rejection")
	}
}

func TestMergeBaseFieldPrecedence(t *testing.T) {
	base := map[string]any{
		"storage_name":          "Base Storage",
		"manifest_ttl_seconds":  float64(120),
		"rewrite_manifest_urls": false,
	}

	// Empty instance: everything comes from the base.
	merged := flutter.MergeBase(base, nil)
	if merged["storage_name"] != "Base Storage" {
		t.Fatalf("expected base storage_name, got %v", merged["storage_name"])
	}

	// Registry overrides one field; other base fields still apply.
	merged = flutter.MergeBase(base, map[string]any{"storage_name": "Instance Storage"})
	if merged["storage_name"] != "Instance Storage" {
		t.Fatalf("registry must win, got %v", merged["storage_name"])
	}
	if merged["manifest_ttl_seconds"] != float64(120) {
		t.Fatalf("expected base manifest TTL to survive, got %v", merged["manifest_ttl_seconds"])
	}

	// Nil base must not break anything.
	merged = flutter.MergeBase(nil, map[string]any{"storage_name": "Only Instance"})
	if merged["storage_name"] != "Only Instance" || len(merged) != 1 {
		t.Fatalf("unexpected merge with nil base: %v", merged)
	}

	// Parsing the merged result must succeed and resolve the precedence end to end.
	cfg, err := flutter.ParseConfig(merged)
	if err != nil {
		t.Fatalf("parse merged config: %v", err)
	}
	if cfg.StorageName != "Only Instance" {
		t.Fatalf("expected instance storage name, got %q", cfg.StorageName)
	}
}

func TestMergeBaseDownloadStorageCategories(t *testing.T) {
	base := map[string]any{
		"download_storage": []any{
			map[string]any{"name": "flutter_infra_release", "manifest_ttl_seconds": float64(120)},
			map[string]any{"name": "dart-archive", "enabled": false},
		},
	}
	instance := map[string]any{
		"download_storage": []any{
			map[string]any{"name": "flutter_infra_release", "manifest_ttl_seconds": float64(600)},
		},
	}

	cfg, err := flutter.ParseConfig(flutter.MergeBase(base, instance))
	if err != nil {
		t.Fatalf("parse merged: %v", err)
	}

	byName := map[string]flutter.DownloadStorageSetting{}
	for _, d := range cfg.DownloadStorage {
		byName[d.Name] = d
	}
	infra, ok := byName["flutter_infra_release"]
	if !ok {
		t.Fatal("expected flutter_infra_release category")
	}
	if infra.ManifestTTLSeconds != 600 {
		t.Fatalf("registry TTL must win, got %d", infra.ManifestTTLSeconds)
	}

	// A base-level disable policy must survive a registry that never mentions that category.
	arch, ok := byName["dart-archive"]
	if !ok {
		t.Fatal("expected base-only dart-archive category to be preserved")
	}
	if arch.Enabled == nil || *arch.Enabled {
		t.Fatalf("expected dart-archive to stay disabled from base, got %+v", arch)
	}
	for _, p := range cfg.AllowedPrefixes {
		if p == "dart-archive" {
			t.Fatalf("disabled category must not appear in allowed_prefixes: %v", cfg.AllowedPrefixes)
		}
	}
}

func TestMergeBaseDownloadStorageReplaceMode(t *testing.T) {
	base := map[string]any{
		"download_storage": []any{
			map[string]any{"name": "dart-archive", "enabled": false},
		},
	}
	instance := map[string]any{
		"download_storage_mode": "replace",
		"download_storage": []any{
			map[string]any{"name": "flutter_infra_release"},
		},
	}

	cfg, err := flutter.ParseConfig(flutter.MergeBase(base, instance))
	if err != nil {
		t.Fatalf("parse merged: %v", err)
	}
	if cfg.DownloadStorageMode != "replace" {
		t.Fatalf("expected replace mode preserved, got %q", cfg.DownloadStorageMode)
	}
	// With replace mode the base's disabled dart-archive must NOT leak in; canonical categories are
	// still appended by ParseConfig so the registry keeps functioning.
	for _, d := range cfg.DownloadStorage {
		if d.Name == "dart-archive" && d.Enabled != nil && !*d.Enabled {
			t.Fatalf("replace mode must not inherit base category policy")
		}
	}
}

func TestDiffBaseSources(t *testing.T) {
	base := map[string]any{
		"storage_name":         "Base",
		"manifest_ttl_seconds": float64(120),
		"download_storage": []any{
			map[string]any{"name": "dart-archive", "enabled": false},
		},
	}
	instance := map[string]any{
		"storage_name": "Instance",
		"download_storage": []any{
			map[string]any{"name": "flutter_infra_release", "manifest_ttl_seconds": float64(60)},
		},
	}

	diffs := flutter.DiffBase(base, instance)
	byKey := map[string]flutter.FieldDiff{}
	for _, d := range diffs {
		byKey[d.Key] = d
	}

	if got := byKey["storage_name"]; got.Source != "registry" || !got.CanRevert {
		t.Fatalf("expected storage_name from registry and revertible, got %+v", got)
	}
	if byKey["storage_name"].InSync {
		t.Fatal("registry value differs from base, InSync must be false")
	}
	if got := byKey["manifest_ttl_seconds"]; got.Source != "base" {
		t.Fatalf("expected manifest_ttl_seconds from base, got %+v", got)
	}
	if got := byKey["serve_stale_on_error"]; got.Source != "default" {
		t.Fatalf("expected serve_stale_on_error from default, got %+v", got)
	}

	cats := byKey["download_storage"].Categories
	if len(cats) != 2 {
		t.Fatalf("expected 2 categories in diff, got %d (%+v)", len(cats), cats)
	}
	found := false
	for _, c := range cats {
		if c.Name == "dart-archive" && c.Source == "base" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected dart-archive sourced from base, got %+v", cats)
	}
}

func TestEffectiveConfigWithProvider(t *testing.T) {
	instance := map[string]any{"storage_name": "Instance"}

	// nil provider -> instance config untouched.
	if got := flutter.EffectiveConfig(instance, nil, context.Background()); got["storage_name"] != "Instance" {
		t.Fatalf("nil provider must not change config, got %v", got)
	}

	// provider returning a base -> merged.
	provider := stubDefaults{"storage_name": "Base", "manifest_ttl_seconds": float64(120)}
	got := flutter.EffectiveConfig(instance, provider, context.Background())
	if got["storage_name"] != "Instance" {
		t.Fatalf("instance must win, got %v", got["storage_name"])
	}
	if got["manifest_ttl_seconds"] != float64(120) {
		t.Fatalf("expected base TTL, got %v", got["manifest_ttl_seconds"])
	}

	// provider returning nothing -> instance config untouched.
	if got := flutter.EffectiveConfig(instance, stubDefaults{}, context.Background()); len(got) != 1 {
		t.Fatalf("empty base must not add keys, got %v", got)
	}
}

func TestBaseSpecHooks(t *testing.T) {
	spec := flutter.BaseSpec()
	if spec == nil || spec.Normalize == nil || spec.Merge == nil || spec.Diff == nil || spec.Seed == nil {
		t.Fatal("expected a fully populated BaseConfigSpec")
	}
	base := map[string]any{"upstream_url": "https://storage.flutter-io.cn", "storage_name": "Base"}
	seed := spec.Seed(base)
	if _, ok := seed["upstream_url"]; ok {
		t.Fatalf("seed must drop base-only upstream_url: %v", seed)
	}
	if seed["storage_name"] != "Base" {
		t.Fatalf("seed must keep instance-config keys: %v", seed)
	}
	if got := flutter.BaseUpstream(base); got != "https://storage.flutter-io.cn" {
		t.Fatalf("expected base upstream, got %q", got)
	}
}

// stubDefaults implements registry.DefaultsProvider for tests.
type stubDefaults map[string]any

func (s stubDefaults) BaseConfig(context.Context, string) (map[string]any, error) {
	if len(s) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out, nil
}
