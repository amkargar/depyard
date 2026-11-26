# npm Registry Support

Depyard implements the npm Registry HTTP API as an isolated registry type
(`backend/internal/registry/npm`). It is registered in the composition root like
every other ecosystem; no shared core package knows about npm.

## Repository modes

| Mode | Behaviour |
|---|---|
| `proxy` | Mirrors packuments from the configured upstream and downloads tarballs on demand. Publishing and unpublishing are rejected. |
| `hosted` | Accepts `npm publish` and serves only locally published tarballs. No upstream requests. |
| `hybrid` | Serves local versions alongside mirrored ones. Protected package patterns are never fetched from upstream. |

The default seeded instance is `npm-packages` (hybrid, upstream
`https://registry.npmjs.org`). Disable it with `SEED_NPM=false` or change it with
`DEFAULT_NPM_SLUG`, `DEFAULT_NPM_NAME`, `DEFAULT_NPM_MODE`, and
`DEFAULT_NPM_UPSTREAM_URL`.

## Client setup

```bash
npm config set registry http://localhost/r/npm-packages/
npm install lodash
npm install @types/node
```

Every registry instance is isolated under `/r/<slug>`. Root-level compatibility
routing is deliberately **not** claimed by npm: npm package paths look like
ordinary SPA routes, so using a dedicated hostname (Instance → Hostname) or the
`/r/<slug>` prefix keeps UI and protocol routes unambiguous.

## Protocol endpoints

| Endpoint | Purpose |
|---|---|
| `GET /-/ping` | Liveness check (`{"ok":true}`). |
| `GET /-/v1/search?text=...&size=...` | Local search in npm's search response format. |
| `GET /{package}` | Full packument (`application/json`). |
| `GET /{package}` with `Accept: application/vnd.npm.install-v1+json` | Abbreviated install packument (name, version, dist only). |
| `GET /{package}/-/{file}.tgz` | Tarball; mirrored from upstream on a cache miss. |
| `PUT /{package}` | `npm publish`. Requires `publish` permission. |
| `DELETE /{package}` (also `/-rev/<revision>`) | Unpublish a whole package. Requires `delete` permission. |
| `DELETE /{package}/-/{file}.tgz` | Unpublish one version. |
| `GET /api/packages` / `GET /api/packages/{package}` | Explorer API used by `/explore/<slug>`. |

Scoped names may be sent either URL-escaped (`@acme%2Ftools`) or with a literal
slash (`@acme/tools`); both resolve to the same lowercase canonical name. All
`dist.tarball` URLs in served packuments point back to the registry instance, not
to upstream.

## Publishing

```bash
# Create a token with the publish scope in the admin console, then:
npm config set //localhost:8080/r/npm-packages/:_authToken mr_your_token
npm publish --registry http://localhost:8080/r/npm-packages/
```

HTTP Basic (`_auth`) works too; the password is treated as the access token, the
same convention Python tooling uses. Unpublishing requires a token with the
`delete` scope and namespace access to the package.

Publish restrictions:

* Versions are immutable — publishing an existing version returns HTTP 403.
* The request must contain a `.tgz` attachment. Its `length`, `shasum` and
  `integrity` are checked when `verify_integrity` is enabled (the default).
* Computed `shasum` (SHA-1) and `integrity` (SHA-512) are added to the stored
  manifest so clients receive correct integrity metadata even if the publisher
  omitted them.
* Tarballs are stored as protected hosted blobs; cache eviction can never delete
  them. Per-registry storage quotas still apply.

## Caching and upstream safety

On a packument miss, the registry fetches the upstream document once and records
its version metadata. Tarballs stay un-cached until a client requests them, at
which point the bytes are streamed into the shared content-addressed store.
Concurrent downloads of the same version are deduplicated, and repeated npm
installs are served locally (`X-Registry-Cache: HIT`).

Upstream tarball URLs advertised by a packument must use the same host as the
configured upstream URL. This prevents a malicious or compromised upstream from
using the mirror as an open HTTP proxy. SHA-512 integrity advertised upstream is
verified while the tarball streams into storage; a mismatch discards the blob.

## Configuration

Settings live in the registry instance's `config` object and are validated by
`npm.ParseConfig` (unknown fields are rejected):

| Key | Type | Default | Description |
|---|---|---|---|
| `upstream_timeout_seconds` | number | `60` | Timeout for packument and tarball requests (0–600). |
| `max_tarball_size_bytes` | number | `104857600` | Maximum size of a single tarball (0–1 GiB). |
| `allowed_packages` | string list | `[]` | Allow-list; empty allows all. Patterns support `*` and trailing `*`. |
| `blocked_packages` | string list | `[]` | Denied package patterns. |
| `protected_packages` | string list | `[]` | In hybrid mode, never mirror these from upstream. |
| `include_prerelease` | boolean | `false` | Allow a pre-release to be selected as `latest`. |
| `verify_integrity` | boolean | `true` | Verify `shasum`/`integrity` on publish. |

Per-registry upstream overrides (auth, proxy, TLS, timeout, custom headers) use
the shared `upstream_config` and work for npm as they do for every other type.

## Metadata and admin operations

* Metadata is stored in the `npm_packages` collection, keyed by
  `(registry_id, name)`. Each document embeds its version list so publish and
  unpublish are single-document updates. In-memory mode uses `npm.MemoryStore`.
* The admin artifact table uses `registry.ArtifactBrowser`; deleting an artifact
  removes its blob reference and metadata.
* `PurgeCache` evicts only cached blobs and mirrored versions; hosted versions
  survive. `CacheWarmer` mirrors a package's metadata plus its latest tarball.
* Deleting a registry instance removes all `npm_packages` documents for that
  `registry_id` through `registry.RegistryCleaner`.

## Tests

```bash
cd backend && go test ./internal/registry/npm/... ./internal/server/...
```

The unit tests cover semver ordering, name/path validation, config validation,
proxy packuments, tarball caching, publish integrity checks, scoped packages,
authorization, admin operations, and the full `/r/<slug>` routing path.
