# Adding a New Registry Type

Depyard is a multi-format package and artifact registry platform where each package ecosystem is compiled into the server as an isolated Go package under `backend/internal/registry/<ecosystem>/`.

The shared core (`internal/registry`, `internal/storage`, `internal/cache`, `internal/upstream`, `internal/auth`, `internal/admin`, `internal/metrics`, `internal/server`) is completely ecosystem-agnostic and must **never** import a concrete registry implementation. You do not need to read or understand the `pub` module to implement a new registry type.

---

## 1. Architecture & Dependency Direction

```
backend/cmd/server (composition root — registers installed types)
        │
        ▼
backend/internal/registry/<ecosystem>/  (isolated protocol + metadata)
        │
        ▼
Shared Core Services (storage, cache, upstream, auth, metrics, audit, registry)
        │
        ▼
Persistence & Infrastructure (MongoDB, filesystem blob store)
```

Create a new isolated package under `backend/internal/registry/<type>/` (for example, `backend/internal/registry/gomod/`).

---

## 2. Declaring Capabilities & Minimal Contract

Every registry type implements the minimal `registry.Registry` contract in `backend/internal/registry/instance.go`:

```go
type Registry interface {
    Type() string
    Capabilities() Capabilities
    Handler() http.Handler
}
```

Declare only the capabilities your ecosystem genuinely supports:

```go
func Capabilities() registry.Capabilities {
    return registry.Capabilities{
        Proxy:             true,
        Hosted:            false, // Proxy-only example
        Publish:           false,
        Search:            false,
        ListVersions:      true,
        CacheWarm:         false,
        MetadataSync:      true,
        ArtifactDeletion:  true,
        VersionRetraction: false,
        PackageOwnership:  false,
    }
}
```

The platform automatically:
- Derives allowed repository modes (`proxy`, `hosted`, `hybrid`) from `Capabilities.Proxy` and `Capabilities.Hosted`.
- Gates Admin API endpoints and Admin Dashboard UI actions on `EffectiveCapabilities` (narrowed by the instance's selected mode). Unsupported actions are never exposed.

### Optional Capability Interfaces

Implement these **only** when your registry supports them — never add empty stub methods:

- `registry.ArtifactBrowser` (`ListArtifacts`) — populates the shared admin artifact table using `registry.ArtifactDescriptor`.
- `registry.ArtifactDeleter` (`DeleteArtifact`) — enables admin artifact deletion when `Capabilities.ArtifactDeletion` is true.
- `registry.CachePurger` (`PurgeCache`) — clears registry-specific cached metadata alongside shared blob cache eviction.
- `registry.CacheWarmer` (`WarmArtifact`) — enables admin cache warming when `Capabilities.CacheWarm` is true.
- `registry.HealthChecker` (`CheckHealth`) — probes upstream availability for monitoring.
- `registry.CompatPathMatcher` (`MatchesCompatPath`) — optional root-level compatibility routing when marked as default.
- `registry.RegistryCleaner` (`CleanupRegistry`) — deletes ecosystem-specific metadata when an instance is removed.
- `registry.Closer` (`Close`) — releases background resources on instance reload or deletion.
- `registry.VersionRegistryProvider` (`VersionRegistry`) — exposes an ecosystem version catalogue (see the Flutter Storage implementation for a per-version registry that documents every installable SDK release and the storage settings used to resolve it). Core code only knows the interface: the Admin API route `GET /api/admin/registries/{id}/version-registry` proxies whatever JSON snapshot the ecosystem returns.

### Ecosystem settings registries

Ecosystem-specific settings live in `Instance.Config` and are validated by the type's `ValidateConfig` function. `ConfigHints` describes each setting for the generic Admin form renderer (`string`, `number`, `boolean`, `string_list`). Structured, nested settings (such as Flutter Storage's per-category `download_storage` array) are supported too: validate them in `ValidateConfig`, expose their catalogue through `ConfigHints`, and render them with a dedicated component that is swapped in by registry type in the Admin UI (see `frontend/components/shared/FlutterStorageSettings.vue`). Shared core packages never learn about the concrete schema.

---

## 3. Registering a Registry Type

Expose a `Registration` function from your package that describes the type, declares capabilities, validates ecosystem-specific config, and constructs configured instances:

```go
func Registration(metaStore MetadataStore) registry.Registration {
    return registry.Registration{
        Type:         "gomod",
        DisplayName:  "Go Modules",
        Description:  "Go Module Proxy (GOPROXY protocol).",
        Capabilities: Capabilities(),
        ConfigHints: []registry.ConfigFieldHint{
            {
                Key:          "upstream_timeout_seconds",
                Label:        "Upstream Timeout (seconds)",
                Type:         "number",
                DefaultValue: 30,
            },
        },
        ValidateConfig: func(cfg map[string]any) error {
            _, err := ParseConfig(cfg) // Reject unknown or invalid fields
            return err
        },
        Create: func(ctx context.Context, inst *registry.Instance, deps registry.ServiceDeps) (registry.Registry, error) {
            return New(inst, deps, metaStore)
        },
    }
}
```

Then register it once in the composition root (`backend/cmd/server/main.go`):

```go
if err := regManager.RegisterType(gomod.Registration(gomodStore)); err != nil {
    logger.Error("failed to register gomod registry type", "error", err)
    os.Exit(1)
}
```

Do **not** register types dynamically at runtime or import your package anywhere inside `backend/internal/*`.

---

## 4. Creating Registry Instances

Once registered, administrators can create one or more instances of your registry type via:
- The Admin Dashboard (**Registries -> New Registry**), which automatically lists installed types from `GET /api/admin/registry-types`.
- `POST /api/admin/registries` with:
  - `slug` (e.g. `go-public`, `go-internal`),
  - `type` (`"gomod"`),
  - `mode` (validated against your declared capabilities),
  - `upstream_url`, `visibility`, `auth_policy`, `storage_quota_bytes`, `cache_policy`,
  - `config` (validated by your `ValidateConfig` function).

Instances are persisted in the shared `registries` MongoDB collection and activated immediately without restarting the server.

---

## 5. Implementing Protocol Handlers

The shared HTTP router mounts every instance under `/r/{slug}/...` (and optionally under a dedicated `Hostname` for protocols such as OCI that require root-level host routing).

Before calling `Handler().ServeHTTP`, the router:
- Verifies the registry instance exists and is enabled (`503` if disabled).
- Strips `/r/{slug}` while preserving `r.URL.RawPath`, percent-encoding (e.g., `%2F` or Go `!` case-escaping), query strings, and headers.
- Injects the instance's external base URL into request context via `registry.BaseURLFromContext(r.Context())`.

Inside your `Handler()`, implement your ecosystem's native routes, content types, error payloads, and authentication challenges:

```go
func (g *GoModRegistry) Handler() http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /{path...}", g.handleProxy)
    return mux
}
```

Keep version parsing, ordering, naming rules, and metadata formatting strictly inside your registry package.

---

## 6. Enforcing Registry Permissions

Authentication is centralized in `deps.Auth` (`*auth.Service`), while protocol-specific challenge headers (e.g., `WWW-Authenticate`) and error payloads remain inside your handler:

```go
principal, err := g.deps.Auth.AuthenticateRequest(r.Context(), r)
if err != nil {
    // Return protocol-appropriate 401 response
    return
}

err = g.deps.Auth.Authorize(principal, auth.AuthRequest{
    Policy:     g.inst.AccessPolicy(),
    Permission: auth.PermissionRead, // or PermissionPublish / PermissionDelete
    Namespace:  modulePath,          // checked against token namespace globs
})
if errors.Is(err, auth.ErrUnauthenticated) {
    // Return 401 with ecosystem challenge header
    return
}
if err != nil {
    // Return 403 Forbidden
    return
}
```

Always call `Authorize` **before** reading or writing artifacts. Authorization checks enforce registry instance isolation, registry type isolation, permission scopes, and namespace globs automatically.

---

## 7. Using Shared Storage, Caching, Upstream & Monitoring

All binary blobs are stored via `deps.Storage` (`*storage.Service`), which is completely ecosystem-agnostic:

### Writing an Artifact

```go
res, err := g.deps.Storage.PutArtifact(ctx, storage.PutArtifactInput{
    RegistryID:       g.inst.ID,
    ArtifactKey:      modulePath + "@" + version + ".zip", // Opaque key owned by your registry
    Origin:           storage.OriginCached,                 // Use OriginHosted for local publishes
    ExpectedDigest:   expectedSHA256,                       // Optional integrity check
    MaxArtifactBytes: g.cfg.MaxArchiveSizeBytes,
    RegistryQuota:    g.inst.StorageQuotaBytes,
    Reader:           upstreamResp.Body,
})
```

- `OriginHosted` automatically sets `Protected: true` so proxy cache eviction can **never** delete locally published artifacts.
- `OriginCached` marks the artifact evictable by TTL/LRU policies.
- Identical binary blobs are physically deduplicated across registries, while logical quota and authorization remain strictly per-registry.

### Streaming an Artifact

```go
stopTransfer := g.deps.Metrics.BeginTransfer(g.inst.ID)
defer stopTransfer()

file, ref, err := g.deps.Storage.OpenArtifact(ctx, g.inst.ID, key)
if err != nil {
    // Handle miss or error
}
defer file.Close()

written, _ := io.Copy(w, file)
g.deps.Metrics.RecordDownload(g.inst.ID, written, cacheHit)
```

### Deduplicating Concurrent Upstream Downloads

Use `deps.Cache.Do(g.inst.ID, key, fn)` (backed by `singleflight`) so concurrent requests for the same uncached artifact trigger only one upstream download via `deps.Upstream.Get(...)`.

### Safe Deletion

Call `g.deps.Storage.DeleteArtifactRef(ctx, g.inst.ID, key)`. The storage service acquires a per-digest lock and removes the physical blob file **only** when zero references remain across all registry instances.

---

## 8. Persisting Ecosystem Metadata

Do **not** force your ecosystem into a shared package schema. Create dedicated MongoDB collections scoped to `registry_id` (for example, `gomod_modules` and `gomod_versions`):

- Index every collection with `registry_id` as the leading compound key (e.g. `{registry_id: 1, module_path: 1}`).
- Scope every query, update, and delete to `g.inst.ID`.
- Implement `CleanupRegistry(ctx)` so deleting a registry instance removes only documents matching `registry_id: g.inst.ID`.
- Pass `*mongo.Database` into your store constructor in `backend/cmd/server/main.go`.

---

## 9. Frontend Integration

The Admin Dashboard (`/admin/*`) is already registry-agnostic — your new registry type will automatically appear in:
- **Registries -> New Registry** (using `TypeInfo`, `SupportedModes`, and `ConfigHints`),
- Per-registry statistics, quota bars, cache controls, and capability-gated artifact tables,
- Scoped access token creation and audit logs.

To add a custom public Package Explorer tailored to your ecosystem:

1. Create isolated components under `frontend/components/explorers/<type>/` (e.g., `GoModRegistryExplorer.vue` and `GoModPackageDetail.vue`).
2. Register them in the static mapping `frontend/utils/registryExplorers.ts`:

```ts
export const REGISTRY_EXPLORERS: Record<string, RegistryExplorerMapping> = {
  pub: { label: 'Dart / Flutter Pub', explorer: PubRegistryExplorer, detail: PubPackageDetail },
  gomod: { label: 'Go Modules', explorer: GoModRegistryExplorer, detail: GoModPackageDetail },
}
```

The `/explore/[slug]` and `/explore/[slug]/packages/[name]` pages automatically dispatch to your components based on `registry.type`.

---

## 10. Testing Protocol Compatibility & Isolation

Write tests covering:

1. **Unit tests (`internal/registry/<type>/<type>_test.go`)**:
   - Ecosystem-specific naming, version parsing/ordering, config validation, and archive/metadata parsing.
2. **Protocol & isolation integration tests**:
   - Spin up an `httptest.Server` mock upstream and wire your registration into `server.New(...)`.
   - Test with the real official client (e.g., `GOPROXY=http://.../r/go-public go get ...`) or realistic protocol headers and path encoding.
   - Verify multiple instances of your type coexist under separate slugs, tokens scoped to one instance return `403` on another, quotas are enforced independently, and shared blobs cannot be accessed anonymously from private instances.
3. **Core isolation check**:
   - Run `go test -race ./...` in `backend/`. `TestCoreNeverImportsConcreteRegistry` enforces that no shared core package imports concrete registry packages.
