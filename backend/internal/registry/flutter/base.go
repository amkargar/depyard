package flutter

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/amkargar/depyard/backend/internal/registry"
)

// BaseUpstreamKey is a base-only key holding the default repository root used when a new registry
// is created. It is not part of Config (the upstream lives on Instance.UpstreamURL).
const BaseUpstreamKey = "upstream_url"

// BaseConfigKeys lists every configuration field that can be supplied by the platform-wide base
// Flutter Storage settings. Registry-level values always win over base values.
var BaseConfigKeys = []string{
	"storage_name",
	"allowed_prefixes",
	"download_storage",
	"releases_base_url",
	"registry_ttl_seconds",
	"manifest_ttl_seconds",
	"rewrite_manifest_urls",
	"serve_stale_on_error",
	"max_artifact_size_bytes",
	"upstream_timeout_seconds",
	"download_storage_mode",
}

// BaseAllKeys is BaseConfigKeys plus base-only keys (such as the default upstream URL).
func BaseAllKeys() []string {
	return append(append([]string(nil), BaseConfigKeys...), BaseUpstreamKey)
}

// mergeableKeys is BaseConfigKeys minus the merge-control flag itself.
func mergeableKeys() []string {
	out := make([]string, 0, len(BaseConfigKeys))
	for _, k := range BaseConfigKeys {
		if k == "download_storage_mode" {
			continue
		}
		out = append(out, k)
	}
	return out
}

// NormalizeBaseConfig validates a platform-wide base Flutter Storage config and returns a canonical
// copy containing only known keys. Unknown keys are rejected so the base settings can never drift
// from the runtime schema enforced by ParseConfig.
func NormalizeBaseConfig(raw map[string]any) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	known := make(map[string]bool, len(BaseAllKeys()))
	for _, allowed := range BaseAllKeys() {
		known[allowed] = true
	}
	for k := range raw {
		if !known[k] {
			return nil, fmt.Errorf("unknown flutter storage config field %q", k)
		}
	}
	// upstream_url is base-only: validate it separately and keep it out of the instance-config
	// validator (which rejects unknown keys by design).
	configOnly := make(map[string]any, len(raw))
	for k, v := range raw {
		if k == BaseUpstreamKey {
			up, _ := v.(string)
			up = strings.TrimRight(strings.TrimSpace(up), "/")
			if up != "" && !strings.HasPrefix(up, "http://") && !strings.HasPrefix(up, "https://") {
				return nil, fmt.Errorf("upstream_url must be an http(s) URL")
			}
			continue
		}
		configOnly[k] = v
	}
	// Reuse the strict parser (defaults + range checks + category normalisation) purely as a
	// validator; only the caller-supplied keys are persisted so "unset" stays distinguishable
	// from "explicitly set to the default".
	if _, err := ParseConfig(configOnly); err != nil {
		return nil, err
	}
	// Round-trip through JSON so nested maps/slices are plain, comparable values.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode flutter base config: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode flutter base config: %w", err)
	}
	for k := range out {
		if out[k] == nil {
			delete(out, k)
		}
	}
	return out, nil
}

// MergeBase overlays the platform base settings with a registry's own config.
//
// Precedence, highest first:
//  1. registry config value (anything explicitly present in instance)
//  2. platform base value (from the admin "base Flutter Storage settings" block)
//  3. flutter.DefaultConfig() (compiled-in defaults)
//
// download_storage is merged per category and then per field, so a base-level policy such as
// "dart-archive is disabled" survives a registry that only overrides a TTL on another category.
// A registry may set "download_storage_mode": "replace" to opt out of category merging.
func MergeBase(base, instance map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(instance))
	for _, k := range mergeableKeys() {
		if v, ok := base[k]; ok {
			out[k] = v
		}
	}
	replaceCategories := false
	if mode, ok := instance["download_storage_mode"].(string); ok && strings.EqualFold(strings.TrimSpace(mode), "replace") {
		replaceCategories = true
	}
	for _, k := range mergeableKeys() {
		v, ok := instance[k]
		if !ok {
			continue
		}
		if k == "download_storage" && !replaceCategories {
			out[k] = mergeDownloadStorage(base["download_storage"], v)
			continue
		}
		out[k] = v
	}
	if _, ok := instance["download_storage"]; ok {
		// Keep the merge directive with the merged result so diagnostics can explain it.
		if mode, ok := instance["download_storage_mode"]; ok {
			out["download_storage_mode"] = mode
		}
	} else if mode, ok := base["download_storage_mode"]; ok {
		out["download_storage_mode"] = mode
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeDownloadStorage layers a registry's category list over the base category list.
func mergeDownloadStorage(baseRaw, instanceRaw any) any {
	baseCats := decodeCategoryList(baseRaw)
	instCats := decodeCategoryList(instanceRaw)
	if len(instCats) == 0 {
		return instanceRaw
	}
	if len(baseCats) == 0 {
		return instanceRaw
	}

	merged := make([]map[string]any, 0, len(instCats)+len(baseCats))
	seen := make(map[string]bool, len(instCats))

	for _, inst := range instCats {
		name, _ := inst["name"].(string)
		name = strings.TrimSpace(name)
		seen[name] = true
		var base map[string]any
		for _, b := range baseCats {
			if bName, _ := b["name"].(string); strings.EqualFold(strings.TrimSpace(bName), name) {
				base = b
				break
			}
		}
		if base == nil {
			merged = append(merged, inst)
			continue
		}
		combined := make(map[string]any, len(base)+len(inst))
		for k, v := range base {
			combined[k] = v
		}
		for k, v := range inst {
			combined[k] = v
		}
		merged = append(merged, combined)
	}

	// Base categories the registry never mentioned still apply (base policy wins over absence).
	for _, base := range baseCats {
		name, _ := base["name"].(string)
		if seen[strings.TrimSpace(name)] {
			continue
		}
		merged = append(merged, base)
	}
	return merged
}

// decodeCategoryList normalises any JSON-ish download_storage value into a list of maps.
func decodeCategoryList(raw any) []map[string]any {
	switch v := raw.(type) {
	case nil:
		return nil
	case []map[string]any:
		return v
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case []DownloadStorageSetting:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var out []map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			return nil
		}
		return out
	default:
		b, err := json.Marshal(raw)
		if err != nil {
			return nil
		}
		var out []map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			return nil
		}
		return out
	}
}

// FieldDiff describes how one config field resolves for a registry.
type FieldDiff struct {
	Key        string         `json:"key"`
	Effective  any            `json:"effective"`
	Base       any            `json:"base,omitempty"`
	Source     string         `json:"source"` // "registry", "base" or "default"
	CanRevert  bool           `json:"can_revert"`
	InSync     bool           `json:"in_sync"`
	Categories []CategoryDiff `json:"categories,omitempty"`
}

// CategoryDiff describes one download_storage category's resolution.
type CategoryDiff struct {
	Name      string   `json:"name"`
	Source    string   `json:"source"` // "registry", "base", "registry+base" or "default"
	Overrides []string `json:"overrides,omitempty"`
	Inherits  []string `json:"inherits,omitempty"`
}

// DiffBase computes a field-by-field diff between the base settings and a registry's config.
// It is used by the admin "impact" preview and by the registry editor to badge base vs overridden
// fields.
func DiffBase(base, instance map[string]any) []FieldDiff {
	instance = normalizeKeyMap(instance)
	base = normalizeKeyMap(base)

	effective := MergeBase(base, instance)
	parsed, err := ParseConfig(effective)
	if err == nil {
		effective = configToMap(parsed)
	}

	out := make([]FieldDiff, 0, len(mergeableKeys()))
	for _, k := range mergeableKeys() {
		d := FieldDiff{Key: k}
		instVal, hasInst := instance[k]
		baseVal, hasBase := base[k]

		switch {
		case hasInst:
			d.Source = "registry"
			d.Effective = instVal
			d.CanRevert = true
		case hasBase:
			d.Source = "base"
			d.Effective = baseVal
		default:
			d.Source = "default"
			d.Effective = effective[k]
		}
		if hasBase && hasInst {
			d.Base = baseVal
			d.InSync = reflect.DeepEqual(canonical(instVal), canonical(baseVal))
		}

		if k == "download_storage" {
			d.Categories = categoryDiffs(base, instance)
			d.InSync = categoriesInSync(d.Categories)
		}
		out = append(out, d)
	}
	return out
}

func categoryDiffs(base, instance map[string]any) []CategoryDiff {
	baseCats := decodeCategoryList(base["download_storage"])
	instCats := decodeCategoryList(instance["download_storage"])
	instByName := make(map[string]map[string]any, len(instCats))
	for _, c := range instCats {
		if name, _ := c["name"].(string); strings.TrimSpace(name) != "" {
			instByName[strings.TrimSpace(name)] = c
		}
	}
	baseByName := make(map[string]map[string]any, len(baseCats))
	for _, c := range baseCats {
		if name, _ := c["name"].(string); strings.TrimSpace(name) != "" {
			baseByName[strings.TrimSpace(name)] = c
		}
	}

	names := make([]string, 0, len(instCats)+len(baseCats))
	seen := make(map[string]bool, len(instCats)+len(baseCats))
	for _, c := range instCats {
		name, _ := c["name"].(string)
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, c := range baseCats {
		name, _ := c["name"].(string)
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}

	out := make([]CategoryDiff, 0, len(names))
	for _, name := range names {
		inst, hasInst := instByName[name]
		b, hasBase := baseByName[name]
		d := CategoryDiff{Name: name}
		switch {
		case hasInst && hasBase:
			d.Source = "registry+base"
			d.Overrides = keysPresent(inst, true)
			d.Inherits = keysPresent(inst, false)
			_ = b
		case hasInst:
			d.Source = "registry"
			d.Overrides = keysPresent(inst, true)
		case hasBase:
			d.Source = "base"
			d.Inherits = keysPresent(b, true)
		}
		out = append(out, d)
	}
	return out
}

func keysPresent(m map[string]any, includeName bool) []string {
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		if !includeName && k == "name" {
			continue
		}
		out = append(out, k)
	}
	return out
}

func categoriesInSync(diffs []CategoryDiff) bool {
	for _, d := range diffs {
		if len(d.Overrides) > 0 {
			return false
		}
	}
	return true
}

// StripKeys returns a copy of cfg without the given keys (used to compute "not configured" state).
func StripKeys(cfg map[string]any, keys ...string) map[string]any {
	if len(cfg) == 0 {
		return map[string]any{}
	}
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !drop[k] {
			out[k] = v
		}
	}
	return out
}

// OverlayFields copies only the given fields from src into dst.
func OverlayFields(dst, src map[string]any, fields []string) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	allowed := make(map[string]bool, len(fields))
	for _, f := range fields {
		allowed[f] = true
	}
	for k, v := range src {
		if len(allowed) == 0 || allowed[k] {
			dst[k] = v
		}
	}
	return dst
}

func normalizeKeyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	for k := range out {
		if out[k] == nil {
			delete(out, k)
		}
	}
	return out
}

func canonical(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

func configToMap(c Config) map[string]any {
	b, err := json.Marshal(c)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// EffectiveConfig resolves the config used at runtime for a Flutter registry: the platform base
// settings (when available) are merged underneath the instance config, so fields the instance does
// not set follow the base live. A nil provider, an error, or an empty base all degrade to the
// instance config alone.
func EffectiveConfig(instance map[string]any, provider registry.DefaultsProvider, ctx context.Context) map[string]any {
	if provider == nil {
		return instance
	}
	if ctx == nil {
		ctx = context.Background()
	}
	base, err := provider.BaseConfig(ctx, RegistryType)
	if err != nil || len(base) == 0 {
		return instance
	}
	return MergeBase(base, instance)
}

// BaseSpec returns the ecosystem hook used by the admin panel to manage platform-wide Flutter
// Storage base settings. Core code only sees this interface, never the flutter package.
func BaseSpec() *registry.BaseConfigSpec {
	return &registry.BaseConfigSpec{
		Keys:      BaseConfigKeys,
		Normalize: NormalizeBaseConfig,
		Merge:     MergeBase,
		Diff: func(base, instance map[string]any) any {
			return DiffBase(base, instance)
		},
		Seed: func(base map[string]any) map[string]any {
			return SeedConfig(base)
		},
	}
}

// SeedConfig turns base settings into a starter config for a newly created registry.
// The base-only upstream_url key is dropped (it belongs on Instance.UpstreamURL); callers that need
// it should use BaseUpstream.
func SeedConfig(base map[string]any) map[string]any {
	if len(base) == 0 {
		return nil
	}
	out := make(map[string]any, len(base))
	for k, v := range base {
		if k == BaseUpstreamKey {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// BaseUpstream returns the default repository root from a base config, if any.
func BaseUpstream(base map[string]any) string {
	up, _ := base[BaseUpstreamKey].(string)
	return strings.TrimRight(strings.TrimSpace(up), "/")
}
