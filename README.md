<p align="center">
  <img src="frontend/public/logo.png" alt="Depyard logo" width="88" height="88" />
</p>

<h1 align="center">Depyard</h1>

<p align="center">
  <strong>Your packages. Your infrastructure.</strong><br />
  A self-hosted home for the packages, builds, and artifacts your team depends on.
</p>

<p align="center">
  <a href="https://amkargar.github.io/depyard-website/"><strong>Website</strong></a>
  · <a href="https://amkargar.github.io/depyard-website/docs/">Documentation</a>
  · <a href="https://github.com/amkargar/depyard/releases">Releases</a>
</p>

<p align="center">
  <a href="https://github.com/amkargar/depyard/actions/workflows/ci.yml"><img src="https://github.com/amkargar/depyard/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://github.com/amkargar/depyard/releases/latest"><img src="https://img.shields.io/github/v/release/amkargar/depyard" alt="Latest release" /></a>
  <img src="https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go" alt="Go 1.25+" />
</p>

## What is Depyard?

Depyard is an open-source, self-hosted package and artifact registry platform. It gives your team one place to cache upstream packages and publish your own, without sending your dependency traffic to someone else's service. The backend is written in Go, metadata lives in MongoDB, and the web interface is built with Nuxt 3.

Registries are added and configured in the admin console at runtime. Every instance has its own `/r/<slug>` endpoint, so you can run several registries side by side, each with its own upstream, policies, and access rules.

## Why teams use it

**One home for packages and builds.** Serve language packages, OS repositories, container images, LFS objects and Hub snapshots from isolated registries.

**Proxy, host, or hybrid.** Choose how each registry behaves: cache packages from an upstream, publish your own artifacts, or do both.

**Fast by default.** Content-addressed storage, deduplication, streaming downloads, and singleflight caching keep installs quick even when several clients ask for the same artifact.

**Under your control.** Scoped access tokens, registry-level permissions, generated first-run credentials, and live settings mean you can change behavior without touching code or restarting the service.

## Supported ecosystems

The [implementation checklist and client guide](docs/registry-checklist.md) covers **22 hosted/proxy formats**: Alpine, Cargo, Composer, Conan (1.x and 2), Conda, CRAN, Debian, Docker/OCI, Git LFS, Go modules, Helm, Hugging Face, Ivy, Maven, Nix, npm, NuGet, Pacman, Pub, PyPI, RPM and RubyGems. It distinguishes native publish protocols from hosting a prepared repository tree. Flutter SDK Storage is also available as a separate mirror.

## Quick start

The one-command installer is the fastest path on Linux, macOS, or Windows WSL. It sets up Docker, generates an initial admin password, and preserves your data on upgrades.

```bash
curl -fsSL https://github.com/amkargar/depyard/releases/latest/download/install.sh -o install.sh
bash install.sh
```

Already have Docker? Clone this repository and start the Compose stack instead. The web UI is available at `http://localhost` (or set `BIND_HOST=127.0.0.1 BIND_PORT=8080` to publish only on loopback). See [bind address, HTTPS and PEM certificate configuration](docs/registry-checklist.md#bind-and-tls-examples) and [.env.example](.env.example).

```bash
git clone https://github.com/amkargar/depyard.git
cd depyard
docker compose up --build
```

Sign in as `admin` with the password printed by the installer, or read the generated password once from the backend log on a manual install. Change it right away under **My Account → Profile**. New registries are seeded disabled, so enable the ones you need under **Admin → Registries**.

## Connect your tools

Point your package managers at the registry slugs you enabled. Replace `localhost` with your server's address when deploying elsewhere.

```bash
export PUB_HOSTED_URL="http://localhost/r/pub-public"
export FLUTTER_STORAGE_BASE_URL="http://localhost/r/flutter-storage"
export GOPROXY="http://localhost/r/go-proxy"

pip install --index-url http://localhost/r/python-packages/simple/ requests
npm install --registry http://localhost/r/npm-packages/ lodash
```

## Development

Run the backend tests and start the Nuxt dev server locally:

```bash
cd backend
go test -race ./...
cd ../frontend
npm install
npm run dev
```

The dev server runs at `http://localhost:3000` and proxies API requests to the Go backend. Use `STORAGE_BACKEND=memory go run ./cmd/server` from `backend/` for a quick, database-free start.

## Documentation

All guides and references live on the [Depyard website](https://amkargar.github.io/depyard-website/): [quick install](https://amkargar.github.io/depyard-website/docs/#quick-install), [first sign-in](https://amkargar.github.io/depyard-website/docs/#first-login), [registry types](https://amkargar.github.io/depyard-website/docs/#registries), [security and users](https://amkargar.github.io/depyard-website/docs/#security), and [FAQ](https://amkargar.github.io/depyard-website/docs/#faq).

## Contributing

Issues and pull requests are welcome in the [GitHub repository](https://github.com/amkargar/depyard). Please keep documentation changes on the [website repository](https://github.com/amkargar/depyard-website) so the docs stay in one place.
