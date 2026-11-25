# Base Flutter Storage settings

Platform-wide defaults for every Flutter Storage (`FLUTTER_STORAGE_BASE_URL`) registry, edited at
**`/admin/settings` → Base Flutter Storage Settings**.

---

## Why

Per-registry settings alone mean that changing an organisation-wide upstream — or adding a second
regional registry — requires editing every instance by hand. The base settings block gives you one
place to define the policy and three ways to consume it:

| Consumption | When it applies | Writes stored config? |
|---|---|---|
| **Seed** | Creating a registry (`RegistryFormModal` pre-fill, server-side seed) | No — new registry only |
| **Apply** | Explicit push to existing registries from the admin UI or API | Yes, per selected field |
| **Inherit** | Continuously, for fields a registry leaves unset | No |

## Storage

One field on the existing platform settings document (no new collection):

```go
type PlatformSettings struct {
    // ...
    DefaultFlutterStorageConfig map[string]any `json:"default_flutter_storage_config"`
    FlutterBaseConfigMode       string         `json:"flutter_base_config_mode"` // "defaults" | "inherit"
}
```

`default_flutter_storage_config` is validated by the Flutter registry type's own strict parser
(`flutter.ParseConfig`), reached through `Registration.BaseConfig` (`registry.BaseConfigSpec`). Core and
admin code never import `internal/registry/flutter` — the `extensibility_test.go` architecture guard
enforces this — so a new ecosystem can opt into base settings by registering a `BaseConfigSpec` and
nothing else.

### Keys

| Key | Notes |
|---|---|
| `upstream_url` | **Base-only.** Default repository root; copied to `Instance.UpstreamURL` for new registries. Never written into an instance `config` map. |
| `storage_name`, `releases_base_url` | Copied to new registries |
| `registry_ttl_seconds`, `manifest_ttl_seconds`, `upstream_timeout_seconds`, `max_artifact_size_bytes` | Numeric overrides |
| `rewrite_manifest_urls`, `serve_stale_on_error` | Booleans |
| `allowed_prefixes` | Allowlist |
| `download_storage` | Per-category settings (upstream, prefixes, TTL, rewrite, enabled) |
| `download_storage_mode` | Per-**instance** opt-out of category merging (`"replace"`) |

## Precedence

Resolved field by field, highest first:

```
1. registry config value (anything explicitly present)
2. platform base value
3. flutter.DefaultConfig() built-in default
```

`download_storage` merges **per category, then per field**:

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

Two consequences worth knowing:

- A base-level **disabled** category cannot be resurrected by a registry that merely tunes another
  category. Requests to it return `403 storage category "<name>" is disabled on this registry`.
- Categories only present in the base are appended, so base policy applies uniformly. Set
  `"download_storage_mode": "replace"` on an instance to use its list verbatim instead.

## Modes

### `defaults` (default)

The base seeds new registries only. Existing registries keep their stored values until you apply the
base explicitly. Nothing changes behind your back.

### `inherit`

Fields a registry does not set follow the base **live**. Implementation:

- `ServiceDeps.Defaults` (`registry.DefaultsProvider`) is a late-bound provider (`admin.LiveDefaults`)
  wired in `main.go`, so registries can consult settings without an import cycle.
- Each Flutter registry re-resolves its effective config through `flutter.EffectiveConfig`
  (base merged under instance config) when activated, and polls the base fingerprint every 3s
  (`baseWatchInterval`). A changed fingerprint swaps the effective `Config` atomically
  (`atomic.Pointer[Config]`); close on reload stops the watcher.
- Registry values always win, so an override stays an override.

Saving base settings therefore takes effect in **~3 seconds with no restart, no apply, and no config
rewrite**. Switching back to `defaults` restores built-in defaults for unset fields just as quickly.

## API

| Method | Path | Purpose |
|---|---|---|
| `GET`/`PUT` | `/api/admin/settings` | Read/write `default_flutter_storage_config` + `flutter_base_config_mode` |
| `GET` | `/api/admin/settings/base-config` | List registry types that support base settings |
| `GET` | `/api/admin/settings/base-config/{type}/impact` | Dry-run diff per registry |
| `POST` | `/api/admin/settings/base-config/{type}/apply` | Apply the base to existing registries |

### Impact preview

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost/api/admin/settings/base-config/flutter/impact
```

```jsonc
{
  "type": "flutter", "mode": "fill-missing", "dry_run": true,
  "registries": [{
    "id": "...", "slug": "flutter-storage", "name": "Flutter Storage",
    "changes": [{ "key": "manifest_ttl_seconds", "to": 120 }],
    "field_sources": [
      { "key": "storage_name", "source": "registry", "can_revert": true, "in_sync": false },
      { "key": "manifest_ttl_seconds", "source": "base", "can_revert": false, "in_sync": false }
    ]
  }]
}
```

`source` is `registry`, `base` or `default`; `in_sync` is false when the registry overrides a field
that the base also sets. `categories[]` breaks the storage list down per category
(`registry`, `base`, `registry+base`, `default`).

### Apply

```jsonc
POST /api/admin/settings/base-config/flutter/apply
{
  "registry_ids": [],            // empty = every Flutter registry
  "fields": [],                  // empty = every field present in the base
  "mode": "fill-missing",        // or "overwrite"
  "dry_run": false
}
```

Response mirrors the impact preview plus `applied`, `failed`, and per-registry `changes[].from`/`.to`.

Semantics:

- `fill-missing` — writes only fields the registry has not set. Idempotent: a second run reports
  `applied: 0`.
- `overwrite` — replaces the selected fields (`fields` empty means every base field).
- The merged config is re-validated (`Manager.ValidateTypeConfig`) **before** persisting; an
  incompatible base produces a per-registry `error` and is skipped rather than corrupting the registry.
- Reload goes through `Manager.UpdateInstance`, so changes are live immediately.
- Every non-dry-run apply writes an audit entry `settings.base_config.apply` with target
  `<type>:<mode>`.
- Cached artifacts are never deleted by an apply.

## UI

- **`/admin/settings` → Base Flutter Storage Settings** — the same `FlutterStorageSettings` component
  with `mode="base"` (base-flavoured labels, no "inherit registry upstream" affordances), an
  inheritance-mode toggle, a reset button, and **Apply to existing…** which opens a dialog with mode
  selection, a per-field filter, and a per-registry diff table before confirming.
- **Create registry modal** — Flutter types are pre-filled from the base; in inherit mode the form
  warns that unset fields keep following the base, and a switch turns seeding off.
- **Registry → Versions & Storage** — `base` badges on fields resolved from the base, an "inherits N
  fields" banner, and **revert to base** on overridden fields (which deletes the key so it inherits
  again).

## Back-compat

- No stored settings doc → empty base → merge is a no-op → behaviour identical to before.
- `default_flutter_storage_url` keeps working and is kept in sync with
  `default_flutter_storage_config.upstream_url`.
- Existing instances are never rewritten except by an explicit apply.

## Tests

- `internal/registry/flutter/base_test.go` — normalization, field precedence, per-category merge,
  replace mode, diff sources, provider plumbing, spec hooks.
- `internal/server/flutter_base_integration_test.go` — settings round-trip, invalid base rejection,
  impact dry-run, `fill-missing` apply + idempotency, `overwrite` field filtering, unknown-field
  rejection, live inheritance in both modes, and base-disabled category enforcement (403).
