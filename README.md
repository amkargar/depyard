# Mirror Registry

A lightweight, extensible, self-hosted, multi-format package and artifact registry platform built with Go, MongoDB, and Nuxt 3 (shadcn-vue + Tailwind CSS + TypeScript).

**Pub** (Dart / Flutter) is the first fully implemented registry type, built on top of an ecosystem-agnostic core that allows future package formats (Go Modules, RPM, NPM, OCI, etc.) to be added as isolated Go modules without rewriting shared infrastructure.

## Key Features

- **Multi-Registry Architecture**: Clear separation between *registry types* (compiled Go packages) and *registry instances* (dynamically created & configured in MongoDB without restarting).
- **Strict Core Isolation**: Shared core services (`storage`, `cache`, `upstream`, `auth`, `metrics`, `audit`, `admin`, `server`) have zero dependency on Pub or any concrete ecosystem.
- **Official Dart Pub v2 Compatibility**:
  - Proxy, Hosted, and Hybrid modes with deterministic resolution and hybrid namespace protection.
  - Full `dart pub publish` 3-step multipart upload workflow, version immutability, retraction, discontinuation, and package uploaders.
  - Optional root-level compatibility route (`/api/packages/*`, `/packages/*`) for single-registry users alongside multi-registry `/r/{slug}/...` routing.
- **Content-Addressed Blob Storage**:
  - Atomic streaming writes, SHA-256 integrity verification, per-digest locking, and physical deduplication across registries.
  - Per-registry logical storage quotas, protected hosted blobs immune to proxy cache eviction, and reference-counted safe deletion.
- **Centralized Scoped Auth**: Users, sessions, and access tokens restricted by registry instance, registry type, permission scopes (`read`, `publish`, `delete`, `admin`), and artifact namespace globs.
- **Unified Monitoring & Admin Dashboard**: Global physical vs logical storage with deduplication savings, per-registry traffic, cache hit ratios, upstream health checks, active transfers, audit logs, and a pub.dev-inspired Pub Package Explorer.

## Quick Start (Docker Compose)

```bash
docker compose up --build
```

Nginx serves as the single public entrypoint (default port `80`, override with `HTTP_PORT=8080 docker compose up --build`):
- Serves the statically generated Nuxt SPA directly (no Node.js runtime container in production).
- Streams `/r/`, `/api/`, `/packages/`, and `/healthz` directly to the Go backend with buffering disabled, raw path preservation, and `client_max_body_size 0`.
- Keeps `backend` and `mongodb` isolated on the internal Docker network without exposing host ports.

- **Web UI & Registry Endpoint**: `http://localhost` (or `http://localhost:<HTTP_PORT>`)
- Default bootstrap admin credentials: `admin` / `changeme123` (configurable via `ADMIN_USERNAME` and `ADMIN_PASSWORD`).

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

## Using with Dart / Flutter

```bash
# Multi-registry URL
export PUB_HOSTED_URL="http://localhost/r/pub-public"

# Or root-level compatibility URL (routed to the default Pub instance)
export PUB_HOSTED_URL="http://localhost"

dart pub get
```

For private registries or publishing:

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
