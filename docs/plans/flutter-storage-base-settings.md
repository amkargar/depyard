# Plan — Base Flutter Storage Settings (admin panel)

Status: **implemented** (phases 1–6) — see [../flutter-base-settings.md](../flutter-base-settings.md) for the
user-facing reference. Decisions taken: full base block, card on `/admin/settings`, `fill-missing` default,
and live inheritance included.
Author: implementation session, branch `arena/0cae594f-mirror-registry`

---

## 1. Problem

Today Flutter Storage configuration exists at exactly **one** level: per registry instance.
Every new Flutter registry starts from hardcoded defaults and must be configured one-by-one.

Evidence from the current code:

| Concern | Where it lives today | Gap |
|---|---|---|
| Default upstream | `PlatformSettings.DefaultFlutterStorageURL` (`backend/internal/admin/settings.go:25`) | Single URL. Does not carry any of the storage settings. |
| Upstream prefill | `RegistryFormModal.fetchPlatformDefaults()` (`frontend/components/shared/RegistryFormModal.vue:136`) | Applies to the create form only. |
| Per-category settings | `flutter.Config.DownloadStorage` (`flutter/config.go:85`) | Per instance only. |
| Global upstream networking | `upstream_timeout_seconds`, proxy, UA, TLS (`settings.go:19-23`) | Applies to all ecosystems; Flutter has its own separate `upstream_timeout_seconds`. |
| Seeding | `main.go:180-213` hardcodes TTLs / prefixes / storage | Not driven by settings. |

So: adding a second Flutter registry (e.g. a regional mirror), or changing the org-wide
`flutter_infra_release` upstream, requires editing **every** instance by hand.

**Goal:** a single *base storage settings* block in the admin panel that (a) prefills and seeds new
Flutter registries, (b) can be pushed to existing ones, and optionally (c) acts as the live fallback
for fields a registry does not set.

---

## 2. Design

### Implementation notes (deviations from the original sketch)

- `BaseConfigSpec` lives on `registry.Registration` (with an `internal/registry` architecture guard test
  forbidding `internal/admin` from importing concrete registries), so the admin panel drives base
  settings generically instead of importing `registry/flutter`.
- Live inheritance is wired through `admin.LiveDefaults` on `ServiceDeps.Defaults` (late-bound to break
  the handler↔manager cycle) with a 3s fingerprint poll per registry and an `atomic.Pointer[Config]`.
- Endpoints are generic: `/api/admin/settings/base-config/{type}/{impact|apply}` rather than
  `flutter-storage`-specific paths.
- `upstream_url` is a base-only key handled separately from the strict instance-config schema.

### 2.1 Data model

Add one field to `PlatformSettings` rather than a new collection — settings are already persisted,
loaded at boot, validated, audited and returned by `GET /api/admin/settings`.

```go
// backend/internal/admin/settings.go
type PlatformSettings struct {
    // ... existing fields ...

    // DefaultFlutterStorageConfig is the base FLUTTER_STORAGE_BASE_URL configuration used
    // to seed new Flutter registries and as the source for "apply to existing" actions.
    // Stored as an opaque map so core admin code stays ecosystem-agnostic; validated through
    // the registry type's ValidateConfig.
    DefaultFlutterStorageConfig map[string]any `json:"default_flutter_storage_config,omitempty" bson:"default_flutter_storage_config,omitempty"`

    // Optional knobs for how the base config is consumed.
    FlutterBaseConfigMode string `json:"flutter_base_config_mode,omitempty" bson:"flutter_base_config_mode,omitempty"`
    // "defaults" (seed + manual apply, today's behavior when empty) | "inherit" (unset fields fall back at runtime)
}
```

Why a map and not a typed struct:
- `admin` must not import `registry/flutter` (core stays ecosystem-agnostic). `VersionRegistryProvider`
  in `registry/instance.go:296` already establishes this boundary.
- Validation reuses `Registration.ValidateConfig` (`registry/manager.go:473`) via a new
  `Manager.ValidateTypeConfig(typeName, cfg)` helper — same strict `ParseConfig` that rejects unknown
  keys, so the settings payload cannot drift from the runtime schema.

`DefaultFlutterStorageURL` is kept and derived: when `default_flutter_storage_config.upstream_url`
is set it wins; otherwise the legacy field is used. No breaking change for existing deployments.

### 2.2 Precedence (field-level, registry always wins)

```
effective(registry, field):
  1. registry.Config[field]              (explicit, including explicit zero values)
  2. base.DefaultFlutterStorageConfig[field]
  3. flutter.DefaultConfig()[field]      (built-in)
```

Special case — `download_storage` **merges per category, then per field**:

```jsonc
// base
{ "download_storage": [
    { "name": "flutter_infra_release", "upstream_url": "https://storage.flutter-io.cn", "manifest_ttl_seconds": 120 },
    { "name": "dart-archive", "enabled": false } ] }

// registry
{ "download_storage": [ { "name": "flutter_infra_release", "manifest_ttl_seconds": 600 } ] }

// effective
{ "download_storage": [
    { "name": "flutter_infra_release", "upstream_url": "https://storage.flutter-io.cn", "manifest_ttl_seconds": 600 },
    { "name": "dart-archive", "enabled": false } ] }
```

Categories present only in the base are appended (so base-level policy applies uniformly), unless the
registry sets `"download_storage_mode": "replace"`.

Implemented as a pure function in the flutter package, unit-testable in isolation:

```go
// backend/internal/registry/flutter/config.go
func MergeBase(base map[string]any, instance map[string]any) (map[string]any, error)
func DiffBase(base, instance map[string]any) (unchanged, overridden, missing []string)
```

### 2.3 API

| Method | Path | Change |
|---|---|---|
| `GET` | `/api/admin/settings` | + `default_flutter_storage_config`, `flutter_base_config_mode` |
| `PUT` | `/api/admin/settings` | validates the base config via `ValidateTypeConfig("flutter", …)`; returns 400 with the exact `ParseConfig` error |
| `POST` | `/api/admin/settings/flutter-storage/apply` | **new** — push base settings into existing registries |
| `GET` | `/api/admin/settings/flutter-storage/impact` | **new** — dry run: per-registry field diff, no writes |

Apply request:

```jsonc
{
  "registry_ids": ["..."],          // empty = all Flutter registries
  "fields": ["upstream_url", "manifest_ttl_seconds", "download_storage"],
  "mode": "fill-missing" ,           // "fill-missing" | "overwrite"
  "dry_run": true
}
```

Response: `{ "registries": [ { "id", "slug", "changes": [{field, from, to}], "skipped": [...] } ], "applied": 3 }`

Semantics:
- `fill-missing` (default) only writes fields the registry has not set — safe, idempotent.
- `overwrite` replaces the selected fields; the UI shows the diff and requires confirmation.
- Every apply writes an audit entry `settings.flutter_base.apply` with the changed registry IDs.
- Reload goes through the existing `manager.UpdateInstance` path, so it hot-reloads without restart.

### 2.4 Runtime inheritance (mode = `inherit`) — optional, phase 3

For a registry with no explicit value to follow the base *live*, handlers need settings at request time.
`ServiceDeps` (`registry/instance.go:301`) has no settings today, so this adds:

```go
type DefaultsProvider interface {
    BaseConfig(ctx context.Context, registryType string) (map[string]any, error)
}
// ServiceDeps.Defaults DefaultsProvider  (nil-safe: falls back to the static config)
```

Then `flutter.ParseConfig` gets an effective-config resolution step that consults the provider.
Cost: touches the hot path for every storage request; requires the settings snapshot to be cached in
memory (it already is — `Handler.defaultConfig`, `settingsMu`). Recommend shipping phases 1–2 first and
only doing this if "change base, everything follows immediately" is actually required.

### 2.5 Admin UI

```
/admin/settings
├── General & Routing Compatibility          (existing)
├── Global Upstream Networking & Proxy       (existing)
│   └── Default Pub / Flutter upstream URLs  (existing; Flutter URL now feeds the base config)
├── ★ Default Flutter Storage Settings       (NEW)
│   ├── <FlutterStorageSettings base-mode />  reuse the shipped component, mode="base"
│   ├── "Reset to built-in defaults"
│   └── "Apply to existing registries…"       → modal: scope (all / pick), fill-missing|overwrite, diff table
├── Data Retention                            (existing)
└── Live Upstream Connectivity Test           (existing)
```

Reuse is the key point: `FlutterStorageSettings.vue` already edits the exact config shape
(storage identity, releases base URL, TTLs, rewrite/serve-stale, timeout, max size, per-category cards).
Add one prop:

```ts
mode?: 'instance' | 'base'   // 'base' hides the "inherit from registry upstream" affordances,
                             // and labels upstream as "Default upstream"
```

Supporting changes:
- `frontend/types/registry.ts:328` — add `default_flutter_storage_config` + `flutter_base_config_mode`.
- `RegistryFormModal.fetchPlatformDefaults()` (`:136`) — seed the create form from the base config
  (today it only reads the two upstream URLs). Show a "Using base settings" hint + "Override" toggle.
- Registry editor `pages/admin/registries/[id].vue` — badge each field as `base` / `overridden`, and add
  a **"Revert field to base"** action.
- `main.go:188` seed — build the seeded Flutter instance from base settings instead of hardcoded values.

### 2.6 Back-compat & migration

- No stored settings doc → `default_flutter_storage_config` is nil → merge is a no-op →
  behavior identical to today.
- Existing instances keep their persisted `Config`; nothing is rewritten until an explicit apply.
- `default_flutter_storage_url` continues to work and is used as `upstream_url` when the base config
  omits one. A one-time `Normalize()` copy keeps the two consistent going forward.
- Container env vars (`DEFAULT_FLUTTER_UPSTREAM_URL`) still seed the initial settings doc.

---

## 3. Work breakdown

Phase 1 — base settings storage + validation (backend, ~small)
1. `admin/settings.go`: add the two fields, defaults, `Normalize()` consistency between
   `default_flutter_storage_url` and `default_flutter_storage_config.upstream_url`.
2. `registry/manager.go`: add `ValidateTypeConfig(typeName string, cfg map[string]any) error`.
3. `admin/handler.go` `handleUpdateSettings`: validate base config for type `flutter`; audit the change.
4. `admin/settings_test.go`: round-trip + rejection cases.

Phase 2 — merge semantics (backend)
5. `flutter/config.go`: `MergeBase`, `DiffBase`, `download_storage_mode`; keep `ParseConfig` strict.
6. `flutter/config_test.go`: field precedence, per-category merge, replace mode, disabled-category
   interaction (a base `enabled:false` must survive a registry that only sets a TTL), idempotency.
7. `admin/handler.go` create path: merge base → instance config before `CreateInstance`.
8. `main.go` seed: build from base settings.

Phase 3 — apply / impact endpoints (backend)
9. `POST /api/admin/settings/flutter-storage/impact` (dry run diff).
10. `POST /api/admin/settings/flutter-storage/apply` (`fill-missing` | `overwrite`, `registry_ids`,
    `fields`, `dry_run`), audit-logged, hot-reload via `UpdateInstance`.
11. Tests: audit entry written, no-op on repeat apply, `overwrite` limited to selected fields.

Phase 4 — admin UI
12. `FlutterStorageSettings.vue`: `mode` prop + base labels.
13. `pages/admin/settings.vue`: new card, save wiring, reset, apply dialog with diff table.
14. `RegistryFormModal.vue`: seed from base config + "Override base settings" toggle.
15. `pages/admin/registries/[id].vue`: `base` / `overridden` badges + revert-to-base.
16. `types/registry.ts`: new fields.

Phase 5 — docs
17. `README.md` + `docs/adding-registry.md`: base settings, precedence table, apply semantics.

Phase 6 — optional live inheritance (only if requested)
18. `ServiceDeps.Defaults`, settings-backed provider, `ParseConfig` resolution, cache invalidation on save.

---

## 4. Test plan

- **Unit** — merge/precedence: base-only, instance-only, both, explicit zero values
  (`manifest_ttl_seconds: 0` must not be treated as unset … or must, per a documented rule),
  per-category partial merge, `download_storage_mode: replace`, disabled category preserved.
- **API** — `PUT /api/admin/settings` rejects an unknown key (`ParseConfig` is strict) and a bad URL;
  accepts a valid base config and returns it on `GET`.
- **Apply** — `fill-missing` twice = second is a no-op; `overwrite` changes only the selected fields;
  `dry_run` writes nothing; audit entry present.
- **E2E (live, memory store)** — set base `flutter_infra_release.upstream_url` to the mock upstream,
  create a new registry with no config → serves from that upstream; disable a category in the base →
  new + applied registries return `403 storage category "…" is disabled`.
- **Frontend** — `vue-tsc`, `nuxt typecheck`, `nuxt build` (SSR) render of the settings page with the
  base card; create-modal prefill assertion.

---

## 5. Open questions (need your call)

1. **Inheritance model** — defaults-only (seed + manual apply), or live inheritance for unset fields?
   Live inheritance is strictly more powerful but touches the request hot path (phase 6).
2. **Scope of the base block** — should it cover the full config (all per-category settings), or only
   the upstream URL + global TTLs?
3. **Apply safety** — default `fill-missing` (never clobbers an override) vs `overwrite` with a diff
   confirmation?
4. **Where it lives** — a card on `/admin/settings` (recommended, matches "General & Routing
   Compatibility") or a dedicated `/admin/settings/flutter-storage` page?
