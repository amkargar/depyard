# Go Module Proxy (`GOPROXY`)

The `goproxy` registry type implements the [Go module proxy protocol](https://go.dev/ref/mod#goproxy-protocol),
so a Go toolchain can resolve modules through the mirror instead of reaching `proxy.golang.org`
directly.

---

## Quick start

```bash
# 1. Point the go command at the mirror.
export GOPROXY="http://localhost/r/go-proxy"

# 2. Fetch modules normally — they are proxied and cached on first use.
go get github.com/pkg/errors@v0.9.1
go mod download
```

On Windows PowerShell:

```powershell
$env:GOPROXY="http://localhost/r/go-proxy"
```

The default seeded instance is `go-proxy` (slug), created on first startup when
`SEED_GO_PROXY=true` (the default).

---

## Protocol

The proxy serves exactly the four request shapes the protocol defines:

| Request | Response |
|---|---|
| `GET /r/<slug>/<module>/@v/list` | Plain-text list of versions, one per line, in Go version order |
| `GET /r/<slug>/<module>/@v/<version>.info` | `{"Version":"v1.2.3","Time":"..."}` |
| `GET /r/<slug>/<module>/@v/<version>.mod` | The module's `go.mod` file |
| `GET /r/<slug>/<module>/@v/<version>.zip` | The module source zip |

Anything else returns **404** — never a directory listing, which the protocol forbids.

```bash
curl -s http://localhost/r/go-proxy/github.com/pkg/errors/@v/list
# v0.8.0
# v0.9.1

curl -s http://localhost/r/go-proxy/github.com/pkg/errors/@v/v0.9.1.info
# {"Version":"v0.9.1","Time":"2022-01-02T03:04:05Z"}

curl -s http://localhost/r/go-proxy/github.com/pkg/errors/@v/v0.9.1.mod
# module github.com/pkg/errors
#
# go 1.17
```

### Root-level compatibility

When `ENABLE_ROOT_COMPAT_ROUTE=true` (the default) and the Go proxy is the default
instance of its type, the same endpoints are also served from the host root:

```bash
export GOPROXY="http://localhost"
```

---

## Caching behaviour

| Artifact | Behaviour |
|---|---|
| Version list / metadata | Cached in the metadata store, refreshed after `metadata_ttl_seconds` |
| `.mod` file | Fetched once, then served from the content-addressed blob store |
| `.zip` archive | Streamed to the blob store on first request, then served locally (`X-Registry-Cache: HIT`) |

Downloads are deduplicated through the shared singleflight coordinator, so concurrent
requests for the same module archive result in a single upstream fetch.

---

## Configuration

Per-instance configuration (Admin Console → *Registries → Create/Edit*):

| Key | Type | Default | Description |
|---|---|---|---|
| `upstream_timeout_seconds` | number | `60` | Timeout for upstream Go proxy requests (1–600s) |
| `max_module_size_bytes` | number | `536870912` (512 MiB) | Maximum allowed module zip size |
| `include_prerelease` | boolean | `true` | Include pseudo-versions and pre-release tags in version lists |
| `allowed_modules` | string list | *(empty)* | When set, only module paths matching these patterns are mirrored |
| `blocked_modules` | string list | *(empty)* | Module path patterns denied from being mirrored |
| `protected_modules` | string list | *(empty)* | Module path patterns that are never evicted from the local cache |

Pattern syntax:

| Pattern | Matches |
|---|---|
| `github.com/pkg/errors` | exactly that module |
| `github.com/*` | anything under `github.com/` |
| `github.com/...` | `github.com` and every module below it |
| `*` | everything |

Unknown configuration keys are rejected at creation time.

---

## Module path rules

Module paths are validated before any upstream request:

- Lowercase letters, digits, `-`, `.`, `_`, `~`, `+` and `/` only.
- No empty path elements, no `.` or `..` elements (traversal protection).
- No query strings or fragments.

Uppercase letters are encoded for upstream requests using the `!` scheme the go command
requires (`github.com/Sirupsen/logrus` → `github.com/!sirupsen/logrus`).

---

## Version ordering

Versions are sorted with Go's semantic version rules:

- Pre-release versions sort **before** the corresponding release.
- Build metadata (`+meta`) is ignored for ordering.
- Numeric pre-release identifiers sort before alphanumeric ones.
- Pseudo-versions (`v0.0.0-20210101000000-abcdef123456`) are recognised and can be
  filtered out with `include_prerelease: false`.

---

## Explorer API

The registry also exposes a read-only JSON API used by the public explorer UI:

| Endpoint | Description |
|---|---|
| `GET /r/<slug>/api/modules?page=&limit=&q=` | Paginated module directory |
| `GET /r/<slug>/api/modules/<module path>` | Module metadata plus its version list |

```bash
curl -s http://localhost/r/go-proxy/api/modules | jq '.modules[].path'
curl -s http://localhost/r/go-proxy/api/modules/github.com/pkg/errors | jq '.versions'
```

The same data is rendered in the public **Go Module Proxy** explorer
(`/explore/<slug>`) and per-module pages (`/explore/<slug>/packages/<module>`).

---

## Capabilities

| Capability | Supported | Notes |
|---|---|---|
| Proxy | ✅ | |
| Hosted | ❌ | The GOPROXY protocol has no publish operation |
| Publish | ❌ | |
| Search | ❌ | Use the explorer API instead |
| List versions | ✅ | |
| Cache warm | ✅ | |
| Metadata sync | ✅ | |
| Artifact deletion | ✅ | |
| Version retraction | ❌ | |
| Package ownership | ❌ | |
| Signature verification | ✅ | Upstream `sumdb` is out of scope for the proxy protocol itself |

---

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `SEED_GO_PROXY` | `true` | Seed the default `go-proxy` instance on first startup |
| `DEFAULT_GO_SLUG` | `go-proxy` | Slug of the seeded instance |
| `DEFAULT_GO_NAME` | `Go Module Proxy` | Display name of the seeded instance |
| `DEFAULT_GO_UPSTREAM_URL` | `https://proxy.golang.org` | Upstream Go proxy |

Any other GOPROXY-compatible upstream works, including a regional mirror or another
private instance of this platform:

```bash
DEFAULT_GO_UPSTREAM_URL="http://internal-mirror/r/go-proxy"
```

---

## Tests

- `backend/internal/registry/goproxy/goproxy_test.go` — version ordering, module path
  validation, path escaping, config validation, capabilities, pattern matching.
- `backend/internal/server/go_proxy_integration_test.go` — full protocol round-trip
  against a fake upstream (list / info / mod / zip), cache MISS→HIT transition,
  protocol errors (404 for unsupported files, missing modules and invalid paths),
  and type registration.

```bash
go test ./internal/registry/goproxy/ ./internal/server/ -run "Go" -v
```
