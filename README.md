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

- **Package Explorer & Admin Console**: `http://localhost:3000`
- **Registry API**: `http://localhost:8080`
- Default bootstrap admin credentials: `admin` / `changeme123` (configurable via `ADMIN_USERNAME` and `ADMIN_PASSWORD`).

## Using with Dart / Flutter

```bash
# Multi-registry URL
export PUB_HOSTED_URL="http://localhost:8080/r/pub-public"

# Or root-level compatibility URL (routed to the default Pub instance)
export PUB_HOSTED_URL="http://localhost:8080"

dart pub get
```

For private registries or publishing:

```bash
dart pub token add http://localhost:8080/r/pub-private
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
