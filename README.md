# Mirror Registry

A lightweight, extensible, self-hosted, multi-format package and artifact registry platform built with Go, MongoDB, and Nuxt 3 (shadcn-vue + Tailwind CSS + TypeScript).

Compiled registry modules built on the ecosystem-agnostic core:
- **Pub (`pub`)** — Official Dart & Flutter Pub v2 registry (Proxy, Hosted, Hybrid).
- **Flutter Storage (`flutter`)** — Full `FLUTTER_STORAGE_BASE_URL` caching mirror for Flutter SDK releases, engine artifacts, Dart archives, and Gradle Maven storage (`flutter_infra_release/`, `flutter_infra/`, `download.flutter.io/`, `dart-archive/`).

## Key Features

- **Multi-Registry Architecture**: Clear separation between *registry types* (compiled Go packages) and *registry instances* (dynamically created & configured in MongoDB without restarting).
- **Strict Core Isolation**: Shared core services (`storage`, `cache`, `upstream`, `auth`, `metrics`, `audit`, `admin`, `server`) have zero dependency on concrete registry implementations (enforced by AST unit tests).
- **Official Dart Pub v2 Compatibility**:
  - Proxy, Hosted, and Hybrid modes with deterministic resolution and hybrid namespace protection.
  - Full `dart pub publish` 3-step multipart upload workflow, version immutability, retraction, discontinuation, and package uploaders.
- **Flutter Storage Base URL Mirror (`FLUTTER_STORAGE_BASE_URL`)**:
  - Mirrors `storage.googleapis.com` (or any regional Flutter storage mirror) with path-prefix allowlists and path-traversal protection.
  - **Per-category storage settings registry** for every `FLUTTER_STORAGE_BASE_URL` category (`flutter_infra_release`, `flutter_infra`, `download.flutter.io`, `dart-archive`): independent upstream repository, allowed prefixes, manifest TTL, manifest `base_url` rewriting, cache policy, and enable/disable toggles — all editable live in the Admin Console.
  - **Platform-wide base storage settings** (`/admin/settings` → *Base Flutter Storage Settings*): one block of defaults that seeds new registries, can be pushed into existing ones field-by-field (`fill-missing` or `overwrite`, with a per-registry diff preview), and — in **inherit** mode — supplies any field a registry leaves unset, updating live within seconds without rewriting stored configs. See [docs/flutter-base-settings.md](docs/flutter-base-settings.md).
  - **Per-version Flutter SDK registry**: derives every installable Flutter release (version, channel, Dart SDK version, commit hash, release date, platform archives, SHA-256 checksums) from the upstream `releases_*.json` manifests, and documents the exact storage resolution used for each version. Served at `/r/<slug>/api/registry`, `/r/<slug>/api/registry/versions/<version>`, and `/api/admin/registries/<id>/version-registry`, and rendered in both the Admin Console ("Versions & Storage" tab) and the public Flutter Storage Explorer.
  - Configurable release-manifest source (`releases_base_url`) for regional mirrors, plus configurable version-registry TTL.
  - Automatically rewrites `base_url` in Flutter release manifests (`releases_*.json`) to point to your mirror (also handles CDN-hosted manifests whose `base_url` host differs from the fetch host), with configurable manifest TTL and stale-while-error fallback.
  - Streams large SDK/engine binaries directly from content-addressed storage with HTTP Range (`206 Partial Content`), `HEAD`, singleflight download deduplication, and SHA-256 verification.
- **Go Module Proxy (`GOPROXY`)**:
  - Implements the official Go module proxy protocol: `GET /r/<slug>/<module>/@v/list`, `.info`, `.mod`, and `.zip` — with strict 404s for anything else (never a directory listing).
  - Proxies and caches module metadata, `go.mod` files, and source zips from `proxy.golang.org` or any GOPROXY-compatible upstream, with singleflight download deduplication and a local `X-Registry-Cache: HIT` on repeat downloads.
  - Go-accurate module path validation (traversal protection) and semantic version ordering, including pseudo-version detection.
  - Per-instance policies: allowed / blocked / protected module patterns, module size cap, and pre-release filtering.
  - Ships a public **Go Module Proxy** explorer (`/explore/<slug>`) with a module directory, per-module version tables, and copyable `GOPROXY=` / `go get` snippets. See [docs/go-module-proxy.md](docs/go-module-proxy.md).
- **Python Package Index (PyPI)**:
  - Implements the [Simple Repository API](https://packaging.python.org/en/latest/specifications/simple-repository-api/) (PEP 503 HTML and PEP 691 JSON) with `Accept` content negotiation, so `pip`, `uv` and `poetry` resolve packages through the mirror.
  - Serves PEP 658/PEP 714 core-metadata sidecars, so installers resolve dependencies without downloading whole distributions.
  - Implements the [legacy upload API](https://docs.pypi.org/api/upload/), so `twine upload` publishes directly — including HTTP Basic authentication (`__token__` / API token), the scheme Python tooling uses.
  - Python-accurate **PEP 440** version parsing and ordering (epochs, pre-releases, post-releases, dev releases and local versions) — not semver.
  - Normalizes project names per the packaging spec (`re.sub(r"[-_.]+", "-", name).lower()`), so `Zope.Interface` and `zope_interface` resolve to the same project.
  - Models a release as many distribution files (one sdist plus any number of platform wheels) across three collections: `pypi_projects`, `pypi_releases` and `pypi_files`.
  - Per-instance policies: allowed / blocked / protected project patterns, distribution size cap, pre-release filtering, and PEP 592 yanking support.
  - Ships a public **Python Package Index** explorer (`/explore/<slug>`) with a package directory, per-release file tables, and copyable `pip install` / `twine upload` snippets. See [docs/pypi-registry.md](docs/pypi-registry.md).
- **Multi-Type Root Compatibility & `/r/<slug>` Routing`:
  - Every registry instance operates under its own isolated `/r/<slug>` base path.
  - Optional root-level compatibility routing serves both the default Pub instance (`/api/packages/*`, `/packages/*`) and the default Flutter Storage instance (`/flutter_infra_release/*`, `/flutter_infra/*`, `/download.flutter.io/*`, `/dart-archive/*`) simultaneously.
- **Custom Application-Styled Dropdowns**: every select in the Admin Console and the public explorers uses a custom accessible listbox (`role="combobox"` + `role="listbox"`) instead of the browser's native `<select>` popup, which ignores the app theme and renders inconsistently across operating systems. Full keyboard support (Arrow keys, Home/End, Enter/Space, Escape), check marks on the active value, outside-click dismissal, and viewport-safe positioning.
- **Self-Hosted Roboto UI Font**: Roboto (SIL Open Font License 1.1) is bundled with the frontend under `frontend/public/fonts/roboto` and is the default UI typeface — no external font CDN is required, so air-gapped deployments render identically.
- **Live Admin Settings & Upstream Management**:
  - Edit global platform settings (`site_title`, `public_base_url`, root compatibility toggle, retention days) and global outbound upstream settings (`http`/`https`/`socks5` proxy URL, timeout, User-Agent, TLS verification, default Pub & Flutter upstream URLs) live from `/admin/settings`.
  - Configure per-registry upstream overrides (URL, Bearer/Basic/Custom-Header auth with masked secret storage, proxy override, timeout, TLS skip-verify, custom HTTP headers) and run live connectivity checks from the Admin UI.
- **Content-Addressed Blob Storage**:
  - Atomic streaming writes, SHA-256 integrity verification, per-digest locking, and physical deduplication across registries.
  - Per-registry logical storage quotas, protected hosted blobs immune to proxy cache eviction, and reference-counted safe deletion.
- **Centralized Scoped Auth**: Users, sessions, and access tokens restricted by registry instance, registry type, permission scopes (`read`, `publish`, `delete`, `admin`), and artifact namespace globs.

## Quick Start (Docker Compose)

```bash
docker compose up --build
```

Nginx serves as the single public entrypoint (default port `80`, override with `HTTP_PORT=8080 docker compose up --build`):
- Serves the statically generated Nuxt SPA directly (no Node.js runtime container in production).
- Streams `/r/`, `/api/`, `/packages/`, `/flutter_infra_release/`, `/flutter_infra/`, `/download.flutter.io/`, `/dart-archive/`, and `/healthz` directly to the Go backend with buffering disabled, raw path preservation, and `client_max_body_size 0`.
- Keeps `backend` and `mongodb` isolated on the internal Docker network without exposing host ports.

- **Web UI & Registry Endpoint**: `http://localhost` (or `http://localhost:<HTTP_PORT>`)
- On first startup with an empty database, both `pub-public` (`PUB_HOSTED_URL`) and `flutter-storage` (`FLUTTER_STORAGE_BASE_URL`) are seeded automatically.

### Signing in for the first time

There is no default password. On the very first start the backend creates the
administrator account and either uses `ADMIN_PASSWORD` if you set it, or
generates a random one and prints it **once** in the log:

```bash
# 1. Start the stack
HTTP_PORT=8080 docker compose up --build -d

# 2. Read the generated administrator password (only appears on first boot)
docker compose logs backend | grep "generated for the initial admin"

#    ...or, to choose your own password instead, set it before the first start
#    (minimum 12 characters):
#    ADMIN_PASSWORD='your-strong-password' HTTP_PORT=8080 docker compose up --build -d
```

Open `http://localhost:8080`, sign in as `admin` with that password, then change
it from **My Account → Profile**. Keep the log line out of shared/CI logs until
you have done so.

If you ever lose the password: a fresh database bootstraps a new administrator;
for an existing database, sign in as another administrator and reset it from
**Admin Console → Users**.

## Creating & Managing Admin Users

There are three ways to create an administrator account:

### 1. Automatic Bootstrap Admin (First Startup)

Set the bootstrap environment variables in `docker-compose.yml` (or your environment) before starting the backend for the first time:

```bash
ADMIN_USERNAME=admin
ADMIN_PASSWORD=your-strong-password
ADMIN_EMAIL=admin@example.com
```

When no **active** administrator exists yet, the backend automatically creates this admin account on startup.

If `ADMIN_PASSWORD` is not set, the server generates a strong random password and prints it **once** in the startup log — there is no predictable default credential any more:

```
WARN no ADMIN_PASSWORD configured: a password was generated for the initial admin account
     username=admin password=<generated>
```

Store it, then change it from **My Account → Profile** after signing in. Passwords must be at least 12 characters (`MIN_PASSWORD_LENGTH` can relax the floor for existing deployments).

`ADMIN_PASSWORD` is only read when the account is **created** — changing it later does not reset an existing administrator's password. A value shorter than the minimum makes the server refuse to start rather than come up with a weak credential (the old `admin123` default is rejected outright). See [docs/user-management.md](docs/user-management.md) for the full account-management reference.

### 2. Via the Admin Web UI

1. Sign in at `http://localhost/login` with an existing admin account.
2. Open **Admin Console → Users** (`/admin/users`).
3. Click **Create User**, enter the username, email, and password, set **Role** to **`Admin`**, and click **Create User**.

The same page lets you edit an account (username, email, role, status, and an
optional password reset). The last active administrator cannot be demoted,
disabled, or deleted — the platform refuses the request rather than locking
everybody out.

### Regular Users

A non-administrator signs in at `http://localhost/login` and lands on **My
Account** (`/dashboard`) instead of the admin console. There they can:

- edit their own username and email, and change their own password
  (`/profile`),
- create, revoke and delete **personal access tokens** (`/tokens`),
- browse the public registries published on the instance.

Personal tokens are bound to the owner and can never grant more than the
owner's own permissions: a regular user cannot mint an `admin`-scoped token.

### 3. Via the Admin HTTP API

Authenticate as an existing admin (session cookie or admin Bearer token) and send a `POST /api/admin/users` request:

```bash
# 1. Log in to obtain an admin session token
TOKEN=$(curl -sS -X POST http://localhost/api/admin/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"your-strong-password"}' | jq -r '.token')

# 2. Create a new admin user
curl -sS -X POST http://localhost/api/admin/users \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "username": "ops-admin",
    "email": "ops@example.com",
    "password": "StrongPassword!123",
    "role": "admin"
  }'
```

## Running Pre-Built Release Images (No Build Required)

Tagging a release (e.g. `git tag v1.0.0 && git push origin v1.0.0`) or running the **Release Container Images** workflow manually in GitHub Actions automatically:
1. Verifies Go tests and frontend typecheck.
2. Builds and pushes multi-arch (`linux/amd64`, `linux/arm64`) images to GHCR (`ghcr.io/amkargar/mirror-registry-backend` and `ghcr.io/amkargar/mirror-registry-nginx`).
3. Publishes a GitHub Release with downloadable `.tar.gz` image archives (`linux-amd64` and `linux-arm64`), a pinned `docker-compose.release.yml`, and `SHA256SUMS.txt`.

### Option A: Pull directly from GHCR

Download `docker-compose.release.yml` from the GitHub Release and start:

```bash
HTTP_PORT=8080 docker compose -f docker-compose.release.yml up -d
```

### Option B: Load the Release Archive (Offline / Private GHCR)

Download `docker-compose.release.yml` and the matching `.tar.gz` archive from the GitHub Release assets:

```bash
# Intel/AMD PC (Windows/Linux/Intel Mac) — use linux-arm64 on Apple Silicon Macs
docker load -i mirror-registry-images-v1.0.0-linux-amd64.tar.gz
HTTP_PORT=8080 docker compose -f docker-compose.release.yml up -d
```

Then open `http://localhost:8080` and sign in as `admin`. Read the generated
password once from the log (or set `ADMIN_PASSWORD` to 12+ characters before the
first start):

```bash
docker compose -f docker-compose.release.yml logs backend | grep "generated for the initial admin"
```

## Using with Dart & Flutter

### Pub Packages (`PUB_HOSTED_URL`) + Flutter SDK / Engine Storage (`FLUTTER_STORAGE_BASE_URL`)

```bash
# Multi-registry isolated URLs
export PUB_HOSTED_URL="http://localhost/r/pub-public"
export FLUTTER_STORAGE_BASE_URL="http://localhost/r/flutter-storage"

# Or root-level compatibility URLs (both default Pub and default Flutter Storage work on the same host!)
export PUB_HOSTED_URL="http://localhost"
export FLUTTER_STORAGE_BASE_URL="http://localhost"

flutter doctor
flutter pub get
```

On Windows PowerShell:

```powershell
$env:PUB_HOSTED_URL="http://localhost/r/pub-public"
$env:FLUTTER_STORAGE_BASE_URL="http://localhost/r/flutter-storage"
```

### Flutter Storage settings registry & per-version registry

Each Flutter Storage instance keeps a registry of the `FLUTTER_STORAGE_BASE_URL` categories and the
Flutter SDK versions it can serve:

```bash
# Per-version Flutter SDK registry (versions, channels, download URLs, storage resolution)
curl -s http://localhost/r/flutter-storage/api/registry | jq '.current_release, (.versions | length)'

# A single version entry
curl -s http://localhost/r/flutter-storage/api/registry/versions/3.27.1 | jq

# Admin view (session/Admin token required) with absolute mirror URLs
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost/api/admin/registries/<registry-id>/version-registry | jq
```

The same data is rendered in the Admin Console under **Registries → <instance> → Versions & Storage**
and in the public Flutter Storage Explorer, and is refreshed automatically (default TTL 900s).

### Base Flutter Storage settings (platform-wide defaults)

`/admin/settings` → **Base Flutter Storage Settings** holds one config block for every Flutter Storage
registry. It is stored as `default_flutter_storage_config` and controlled by `flutter_base_config_mode`:

| Mode | Behaviour |
|---|---|
| `defaults` (default) | Seeds the create form and the first-start seed only. Existing registries keep their stored values until you apply the base explicitly. |
| `inherit` | Registries that leave a field unset follow the base **live** — changing the base updates them within seconds, no restart and no config rewrite. |

Precedence is always **registry → base → built-in default**, resolved field by field, with
`download_storage` merged *per category and then per field* (so a base-level
`{"name": "dart-archive", "enabled": false}` policy survives a registry that only overrides another
category's TTL). A registry can opt out of category merging with `"download_storage_mode": "replace"`.

```bash
TOKEN=$(curl -s -X POST http://localhost/api/admin/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"your-strong-password"}' | jq -r .token)

# Save the base settings (validated by the same strict parser the runtime uses)
curl -s -X PUT http://localhost/api/admin/settings \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
    "site_title": "Mirror Registry",
    "default_flutter_storage_url": "https://storage.flutter-io.cn",
    "flutter_base_config_mode": "inherit",
    "default_flutter_storage_config": {
      "upstream_url": "https://storage.flutter-io.cn",
      "manifest_ttl_seconds": 120,
      "download_storage": [{ "name": "dart-archive", "enabled": false }]
    }
  }' | jq '.flutter_base_config_mode, .default_flutter_storage_config'

# Preview what applying the base would change, per registry (never writes)
curl -s -H "Authorization: Bearer $TOKEN" \
  http://localhost/api/admin/settings/base-config/flutter/impact | jq '.registries[] | {slug, changes}'

# Push it into existing registries (fill-missing = never clobber an override)
curl -s -X POST http://localhost/api/admin/settings/base-config/flutter/apply \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"mode":"fill-missing","dry_run":false}' | jq '.applied, .failed'
```

Apply semantics: `fill-missing` (default) writes only fields the registry does not set and is
idempotent — a second run reports `applied: 0`. `overwrite` replaces the selected `fields` (all base
fields when omitted) and the response returns the exact `from`/`to` per field. Every apply is
audit-logged as `settings.base_config.apply` and re-validates the merged config before persisting, so
an incompatible base can never corrupt a live registry. Cached artifacts are never deleted.

For private registries or publishing Dart packages:

```bash
dart pub token add http://localhost/r/pub-private
dart pub publish
```

## Using with Go

### Go Module Proxy (`GOPROXY`)

```bash
# Multi-registry isolated URL
export GOPROXY="http://localhost/r/go-proxy"

# Or root-level compatibility URL (when the Go proxy is the default instance)
export GOPROXY="http://localhost"

go get github.com/pkg/errors@v0.9.1
go mod download
```

The proxy serves the standard protocol endpoints:

```bash
curl -s http://localhost/r/go-proxy/github.com/pkg/errors/@v/list
curl -s http://localhost/r/go-proxy/github.com/pkg/errors/@v/v0.9.1.info
curl -s http://localhost/r/go-proxy/github.com/pkg/errors/@v/v0.9.1.mod
curl -s http://localhost/r/go-proxy/github.com/pkg/errors/@v/v0.9.1.zip
```

See [docs/go-module-proxy.md](docs/go-module-proxy.md) for configuration, caching
behaviour, module path rules, and the explorer API.

## Using with Python

### Python Package Index (PyPI)

```bash
# Multi-registry isolated URL
pip install --index-url http://localhost/r/python-packages/simple/ requests

# Or configure it once
pip config set global.index-url http://localhost/r/python-packages/simple/

# With uv
uv pip install --index-url http://localhost/r/python-packages/simple/ requests
```

Publish a package with `twine`:

```bash
TWINE_USERNAME=__token__ \
TWINE_PASSWORD=mr_your_access_token \
twine upload --repository-url http://localhost/r/python-packages/legacy/ dist/*
```

The index serves the Simple Repository API in both HTML and JSON, plus PEP 658
core-metadata sidecars:

```bash
curl -s http://localhost/r/python-packages/simple/requests/
curl -s -H 'Accept: application/vnd.pypi.simple.v1+json' \
  http://localhost/r/python-packages/simple/requests/
```

See [docs/pypi-registry.md](docs/pypi-registry.md) for configuration, PEP 440 version
rules, publishing, and the explorer API.

## Local Development

### Backend

```bash
cd backend
go test -race ./...

# With MongoDB:
MONGODB_URI="mongodb://localhost:27017" go run ./cmd/server

# Or standalone in-memory mode for quick local UI testing:
STORAGE_BACKEND=memory SEED_DEMO_PACKAGES=true go run ./cmd/server
```

The first start creates the administrator and prints a generated password on the
`no ADMIN_PASSWORD configured` warning line — copy it and sign in at
`http://localhost:3000/login` (the Nuxt dev server proxies `/api` to the
backend). In-memory mode starts from an empty database every run, so that line
reappears on each restart; with MongoDB the account persists and the
password is only generated once.

### Frontend

```bash
cd frontend
npm install
npm run dev
```

## Extending with New Registry Types

See [docs/adding-registry.md](docs/adding-registry.md) for a concise guide on registering a new package ecosystem, implementing protocol handlers, using shared blob storage, enforcing permissions, and integrating a custom explorer UI.
