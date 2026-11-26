# Plan: PyPI / Python Package Index (`pypi`) Registry Type

## Goal

Add a new `pypi` registry type that lets Python tooling (`pip`, `uv`, `poetry`) install
packages from the platform, and lets `twine` publish to it.

The registry type follows the same architecture as the existing `pub` and `goproxy`
implementations: an isolated package under `backend/internal/registry/pypi/`, registered in
the composition root, with its own explorer UI.

---

## 1. Protocol requirements

### 1.1 Simple Repository API (PEP 503 / PEP 691)

Base URL: `http://host/r/<slug>/simple/`

| Request | Response |
|---|---|
| `GET /simple/` | Project index (all project names) |
| `GET /simple/<project>/` | Project detail (all files of one project) |

**Content negotiation (PEP 691).** The `Accept` header selects the serialization:

| Accept | Content-Type |
|---|---|
| `application/vnd.pypi.simple.v1+json` | `application/vnd.pypi.simple.v1+json` |
| `application/vnd.pypi.simple.v1+html` | `application/vnd.pypi.simple.v1+html` |
| `text/html`, `*/*`, absent | `text/html` (PEP 503, the legacy default) |
| `application/vnd.pypi.simple.latest+json` | resolves to the highest supported JSON version |

An unknown/unsupported `Accept` must return **406**, never a 300.

**JSON project index:**
```json
{ "meta": { "api-version": "1.1" }, "projects": [ { "name": "requests" } ] }
```

**JSON project detail (PEP 691 + PEP 700 + PEP 658):**
```json
{
  "meta": { "api-version": "1.1" },
  "name": "requests",
  "files": [
    {
      "filename": "requests-2.31.0-py3-none-any.whl",
      "url": "http://host/r/pypi/packages/requests/2.31.0/requests-2.31.0-py3-none-any.whl",
      "hashes": { "sha256": "..." },
      "requires-python": ">=3.7",
      "yanked": false,
      "dist-info-metadata": { "sha256": "..." },
      "size": 12345,
      "upload-time": "2023-05-22T12:00:00Z"
    }
  ],
  "versions": [ "2.31.0" ]
}
```

**HTML project detail (PEP 503):**
```html
<!DOCTYPE html>
<html><head><meta name="pypi:repository-version" content="1.1"></head><body>
<a href="/r/pypi/packages/requests/2.31.0/requests-2.31.0-py3-none-any.whl"
   data-requires-python="&gt;=3.7"
   data-dist-info-metadata="sha256=..."
   data-core-metadata="sha256=..."
   data-yanked="false">requests-2.31.0-py3-none-any.whl</a>
</body></html>
```

### 1.2 File download

`GET /r/<slug>/packages/<project>/<version>/<filename>` streams the distribution file.

### 1.3 PEP 658 / PEP 714 core metadata

`GET /r/<slug>/packages/<project>/<version>/<filename>.metadata` returns the raw
`METADATA`/`PKG-INFO` file of the distribution.

### 1.4 Upload API (legacy, used by `twine`)

`POST /r/<slug>/legacy/` with `multipart/form-data` and `:action=file_upload`:

| Field | Meaning |
|---|---|
| `:action` | `file_upload` |
| `protocol_version` | `1` |
| `content` | the distribution file |
| `name` | project name |
| `version` | version |
| `filetype` | `bdist_wheel` or `sdist` |
| `pyversion` | python tag for wheels, `source` for sdists |
| `md5_digest` / `sha256_digest` / `blake2_256_digest` | at least one required |
| `metadata_version`, plus any other core metadata field | lowercased, `-` → `_` |

### 1.5 Warehouse-style JSON API (optional, for explorers)

`GET /r/<slug>/pypi/<project>/json` — the classic PyPI JSON shape used by many tools.

---

## 2. Backend design

### 2.1 Package layout

```
backend/internal/registry/pypi/
  pypi.go        — Config, capabilities, Registration, registry type
  version.go     — PEP 440 version parsing, normalization and ordering
  protocol.go    — Simple API response types (JSON + HTML rendering)
  metadata.go    — PyProject / PyRelease / PyFile metadata store (Mongo + memory)
  handler.go     — HTTP handlers (simple API, files, metadata, JSON API)
  upload.go      — legacy upload endpoint + distribution parsing
  mirror.go      — upstream proxying and caching
  registry_ops.go— ArtifactBrowser / ArtifactDeleter / CachePurger / HealthChecker
  explorer.go    — explorer JSON API
```

### 2.2 Data model

A PyPI project has **many files per version** (one sdist + N wheels), unlike the Pub/Go
models which have one artifact per version. So the store needs three collections:

```
pypi_projects   — one document per project (name, display name, summary, urls)
pypi_releases   — one document per (project, version)
pypi_files      — one document per (project, version, filename)
```

```go
type PyProject struct {
    ID, RegistryID string
    Name           string   // normalized (PEP 503): lowercase, [-_.]+ → "-"
    DisplayName    string   // original spelling
    Summary        string
    Description    string
    Homepage       string
    Author         string
    AuthorEmail    string
    License        string
    RequiresPython string
    Classifiers    []string
    ProjectURLs    map[string]string
    Origin         string   // "hosted" | "cached"
    ReleaseCount   int
    FileCount      int
    TotalSizeBytes int64
    Downloads      int64
    LatestVersion  string
    UpstreamSyncedAt *time.Time
    CreatedAt, UpdatedAt time.Time
}

type PyRelease struct {
    ID, RegistryID string
    ProjectName    string
    Version        string   // PEP 440 canonical form
    Origin         string
    RequiresPython string
    Summary        string
    Yanked         bool
    YankedReason   string
    PublishedAt    time.Time
    CreatedAt      time.Time
}

type PyFile struct {
    ID, RegistryID string
    ProjectName    string
    Version        string
    Filename       string
    Packagetype    string   // "bdist_wheel" | "sdist"
    PythonVersion  string
    RequiresPython string
    Digest         string   // sha256 hex
    MD5Digest      string
    SizeBytes      int64
    MetadataDigest string   // sha256 of the core metadata file
    HasMetadata    bool
    Yanked         bool
    Origin         string
    CachedAt       *time.Time
    CreatedAt      time.Time
}
```

### 2.3 PEP 440 version handling

Versions are **not** semver. The parser must implement PEP 440:

```
[N!]N(.N)*[{a|b|rc}N][.postN][.devN][+local]
```

Ordering key: epoch → zero-padded release → pre → post → dev, where

- `dev` < `a` < `b` < `rc` < final < `post`
- a version with only `.devN` sorts **below** every pre-release of the same release
- a missing post sorts **below** every post
- a missing dev sorts **above** every dev
- local versions sort above the same version without a local segment

Normalization per the name-normalization spec:

- **Project names**: `re.sub(r"[-_.]+", "-", name).lower()`
- **Versions**: canonical form strips a leading `v`, normalizes separators, and lowercases.

Both are exposed as exported helpers so they can be unit-tested directly.

### 2.4 Capabilities

| Capability | Value | Reason |
|---|---|---|
| Proxy | ✅ | mirrors an upstream index |
| Hosted | ✅ | `twine upload` is a real workflow |
| Publish | ✅ | legacy upload API |
| Search | ❌ | no PyPI search protocol |
| ListVersions | ✅ | |
| CacheWarm | ✅ | |
| MetadataSync | ✅ | |
| ArtifactDeletion | ✅ | |
| VersionRetraction | ✅ | yanking (PEP 592) |
| PackageOwnership | ✅ | uploaders |
| SignatureVerification | ✅ | hash verification on upload |

### 2.5 Configuration

| Key | Type | Default | Description |
|---|---|---|---|
| `upstream_timeout_seconds` | number | `60` | upstream request timeout |
| `max_file_size_bytes` | number | `104857600` (100 MiB) | max distribution file size |
| `include_prerelease` | boolean | `false` | serve pre-release versions by default |
| `allowed_projects` | string list | *(empty)* | allow-list of project name patterns |
| `blocked_projects` | string list | *(empty)* | deny-list of project name patterns |
| `protected_projects` | string list | *(empty)* | never evicted from cache |
| `require_upload_hash` | boolean | `true` | reject uploads without a digest field |
| `serve_core_metadata` | boolean | `true` | serve PEP 658 `.metadata` files |

---

## 3. Authentication gap (important)

`pip` and `twine` authenticate with **HTTP Basic**, but the platform's auth service only
understands `Authorization: Bearer` and session cookies
(`backend/internal/auth/service.go:285`).

Twine's convention is `username=__token__`, `password=<token>`, so the fix is a
**core change** in `extractBearer` → a new `extractCredentials` that also decodes Basic
credentials and treats the password as a bearer token.

This must live in `internal/auth` (shared core), not in the pypi package, so every
registry benefits and the architecture guard stays green.

```go
// internal/auth/service.go
func extractCredentials(r *http.Request) (username, token string, ok bool)
```

- `Authorization: Bearer <token>` → token
- `Authorization: Basic base64(user:pass)` → token = password (username is informational)
- cookie → session

`twine` also sends `__token__` as the username; the password is the real API token, so
mapping password → bearer token is exactly right.

---

## 4. Frontend design

New explorer components, mirroring the Go/Pub structure:

```
frontend/components/explorers/pypi/PypiRegistryExplorer.vue   — project directory
frontend/components/explorers/pypi/PypiProjectDetail.vue       — project + files table
```

Registered in `frontend/utils/registryExplorers.ts` under `pypi`.

The project detail page shows:
- project summary, homepage, author, license, requires-python
- the release list with each file's filename, size, hash, and Python tag
- copyable `pip install` snippet with the index URL
- copyable `twine upload` snippet

Because PyPI project names are normalized and never contain `/`, the existing
`/explore/:slug/packages/:name` route works unchanged.

---

## 5. Implementation phases

### Phase 1 — Core plumbing
- Add Basic-auth credential extraction to `internal/auth`.
- Add `pypi` type registration, config parsing, capabilities.

### Phase 2 — PEP 440
- `version.go`: parser, canonicalizer, comparison, ordering helpers.
- Unit tests covering the ordering chain from the PEP.

### Phase 3 — Metadata store
- `metadata.go`: `PyProject` / `PyRelease` / `PyFile`, Mongo + memory implementations.

### Phase 4 — Simple API
- `protocol.go`: JSON + HTML rendering, content negotiation.
- `handler.go`: `/simple/`, `/simple/<project>/`, file download, `.metadata`.

### Phase 5 — Upload
- `upload.go`: legacy multipart upload, wheel/sdist inspection, core metadata extraction.

### Phase 6 — Proxying
- `mirror.go`: upstream index mirroring and file caching.

### Phase 7 — Admin + explorer
- `registry_ops.go`, `explorer.go`.
- Frontend explorer components.

### Phase 8 — Wiring, docs, tests
- Composition root, config env vars, seed instance, docker-compose, README, docs page.

---

## 6. Testing strategy

**Unit** — PEP 440 ordering, name normalization, config validation, HTML/JSON rendering.

**Integration** — a fake upstream index; full `pip`-style request flow; content
negotiation matrix; upload round-trip; cache MISS→HIT; protocol errors.

**Live E2E** — a running server with a fake PyPI upstream; `pip download`/`pip install`
against it if a Python interpreter is available in the sandbox.

---

## 7. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Basic auth is a core change affecting all registries | Additive only: existing Bearer/cookie paths unchanged; covered by existing auth tests |
| PEP 440 ordering is subtle | Implement directly from the PEP text; unit-test the full ordering chain |
| Many files per version (unlike other registries) | Dedicated `pypi_files` collection rather than overloading the release document |
| HTML simple index needs correct escaping | Use `html/template` with explicit escaping, never string concatenation |
| Upstream PyPI HTML parsing | Prefer the PEP 691 JSON API upstream (`Accept: application/vnd.pypi.simple.v1+json`), fall back to HTML parsing |

---

## 8. Deliberately out of scope

- **Search API** — PyPI's search is not a standard protocol.
- **Provenance / attestations (PEP 740)** — newer than the baseline; upload accepts and
  ignores the `attestations` field.
- **Sigstore / GPG signature verification** — hashes are verified; signature chains are not.
- **Legacy `/pypi/<name>/<version>/json` beyond the basic shape** — a compatible
  subset only.
