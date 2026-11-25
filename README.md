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
- **Multi-Type Root Compatibility & `/r/<slug>` Routing**:
  - Every registry instance operates under its own isolated `/r/<slug>` base path.
  - Optional root-level compatibility routing serves both the default Pub instance (`/api/packages/*`, `/packages/*`) and the default Flutter Storage instance (`/flutter_infra_release/*`, `/flutter_infra/*`, `/download.flutter.io/*`, `/dart-archive/*`) simultaneously.
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

## Creating & Managing Admin Users

There are three ways to create an administrator account:

### 1. Automatic Bootstrap Admin (First Startup)

Set the bootstrap environment variables in `docker-compose.yml` (or your environment) before starting the backend for the first time:

```bash
ADMIN_USERNAME=admin
ADMIN_PASSWORD=your-strong-password
ADMIN_EMAIL=admin@example.com
```

When the users collection has no admin user yet, the backend automatically creates this admin account on startup (default fallback if unset: `admin` / `changeme123` — change this immediately in production).

### 2. Via the Admin Web UI

1. Sign in at `http://localhost/login` with an existing admin account.
2. Open **Admin Console → Users** (`/admin/users`).
3. Click **Create User**, enter the username, email, and password, set **Role** to **`Admin`**, and click **Create User**.

### 3. Via the Admin HTTP API

Authenticate as an existing admin (session cookie or admin Bearer token) and send a `POST /api/admin/users` request:

```bash
# 1. Log in to obtain an admin session token
TOKEN=$(curl -sS -X POST http://localhost/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"changeme123"}' | jq -r '.token')

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

Then open `http://localhost:8080`.

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
  -d '{"username":"admin","password":"admin123"}' | jq -r .token)

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

### Frontend

```bash
cd frontend
npm install
npm run dev
```

## Extending with New Registry Types

See [docs/adding-registry.md](docs/adding-registry.md) for a concise guide on registering a new package ecosystem, implementing protocol handlers, using shared blob storage, enforcing permissions, and integrating a custom explorer UI.
