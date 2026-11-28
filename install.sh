#!/usr/bin/env bash
set -euo pipefail

# -----------------------------------------------------------------------------
# Depyard installer
#
# Self-contained: the Compose definition is embedded below, so this is the only
# file you need to download. The release workflow stamps INSTALLER_VERSION and
# INSTALLER_IMAGE_PREFIX with the values for the release that ships this script.
# -----------------------------------------------------------------------------

INSTALLER_VERSION="latest"
INSTALLER_IMAGE_PREFIX="ghcr.io/amkargar/depyard"

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

INSTALL_DIR="${DEPWARD_INSTALL_DIR:-$HOME/.depyard}"
HTTP_PORT_EXPLICIT=0
ADMIN_USERNAME_EXPLICIT=0
ADMIN_EMAIL_EXPLICIT=0
[[ -n "${HTTP_PORT+x}" ]] && HTTP_PORT_EXPLICIT=1
[[ -n "${ADMIN_USERNAME+x}" ]] && ADMIN_USERNAME_EXPLICIT=1
[[ -n "${ADMIN_EMAIL+x}" ]] && ADMIN_EMAIL_EXPLICIT=1

HTTP_PORT="${HTTP_PORT:-8080}"
ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@example.com}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
DOCKER_BIN="${DOCKER_BIN:-docker}"

VERSION="${DEPWARD_VERSION:-$INSTALLER_VERSION}"
IMAGE_PREFIX="$INSTALLER_IMAGE_PREFIX"
MODE="online"
UPGRADE=0
INSTALLED_VERSION=''
UNINSTALL=0
PURGE=0
ASSUME_YES=0

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
  BOLD=$'\033[1m'; BLUE=$'\033[34m'; GREEN=$'\033[32m'
  YELLOW=$'\033[33m'; RED=$'\033[31m'; RESET=$'\033[0m'
else
  BOLD=''; BLUE=''; GREEN=''; YELLOW=''; RED=''; RESET=''
fi

info()  { printf '%s==>%s %s\n' "$BLUE" "$RESET" "$*"; }
ok()    { printf '%s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn()  { printf '%s!%s %s\n' "$YELLOW" "$RESET" "$*" >&2; }
fail()  { printf '%serror:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

usage() {
  cat <<'USAGE'
Depyard installer

Usage:
  bash install.sh [options]
  bash install.sh online|offline       (legacy positional form)

Options:
  --offline              Load images from a local release archive instead of GHCR.
  --version <tag>        Install a specific release (e.g. v1.4.0).
  --install-dir <path>   Where to keep .env and the managed Compose file.
  --http-port <port>     Host port for the web UI (default: 8080).
  --upgrade [<version>]  Upgrade an existing install to this script's version,
                         or to the optional version tag.
  --uninstall            Stop and remove containers; keep .env and data volumes.
  --purge                With --uninstall, also delete .env and data volumes.
  --yes                  Skip the --purge confirmation (for automation).
  -h, --help             Show this help.

Environment:
  BIND_HOST, BIND_PORT (overrides HTTP_PORT), HTTP_PORT, PUBLIC_BASE_URL
  TLS_CERT_DIR (host), TLS_CERT_FILE and TLS_KEY_FILE (container paths)
  ADMIN_USERNAME, ADMIN_EMAIL, ADMIN_PASSWORD
  DEPWARD_INSTALL_DIR, DEPWARD_VERSION, NO_COLOR

Installer downloads are hosted on GitHub Releases:
  https://github.com/amkargar/depyard/releases/latest/download/install.sh
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    online) MODE=online; shift ;;
    offline|--offline) MODE=offline; shift ;;
    --version|-v)
      [[ $# -ge 2 ]] || fail "--version needs a value"
      VERSION="$2"; shift 2 ;;
    --install-dir)
      [[ $# -ge 2 ]] || fail "--install-dir needs a value"
      INSTALL_DIR="$2"; shift 2 ;;
    --http-port)
      [[ $# -ge 2 ]] || fail "--http-port needs a value"
      HTTP_PORT="$2"; HTTP_PORT_EXPLICIT=1; shift 2 ;;
    --upgrade)
      UPGRADE=1
      if [[ $# -ge 2 && "$2" =~ ^v?[0-9] ]]; then
        VERSION="$2"; shift 2
      else
        shift
      fi
      ;;
    --uninstall) UNINSTALL=1; shift ;;
    --purge) PURGE=1; shift ;;
    --yes|-y) ASSUME_YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument '$1' (try --help)" ;;
  esac
done

if [[ "$PURGE" -eq 1 && "$UNINSTALL" -eq 0 ]]; then
  fail "--purge only applies with --uninstall"
fi
if [[ "$UPGRADE" -eq 1 && "$UNINSTALL" -eq 1 ]]; then
  fail "--upgrade and --uninstall are mutually exclusive"
fi

[[ "$HTTP_PORT" =~ ^[0-9]+$ ]] || fail "HTTP_PORT must be a number, got '$HTTP_PORT'"
[[ "$VERSION" =~ ^[A-Za-z0-9._-]+$ ]] || fail "invalid version '$VERSION'"
[[ "$ADMIN_USERNAME" =~ ^[A-Za-z0-9._-]+$ ]] || fail "ADMIN_USERNAME may only contain letters, digits, dot, dash and underscore"

if [[ -n "$ADMIN_PASSWORD" ]]; then
  [[ ${#ADMIN_PASSWORD} -ge 12 ]] || fail "ADMIN_PASSWORD must be at least 12 characters"
  [[ "$ADMIN_PASSWORD" =~ ^[A-Za-z0-9_-]+$ ]] || \
    fail "ADMIN_PASSWORD may only contain letters, digits, '-' and '_' (for safe storage in .env)"
fi

command -v "$DOCKER_BIN" >/dev/null 2>&1 || fail "Docker is required but was not found in PATH"
"$DOCKER_BIN" info >/dev/null 2>&1 || fail "Docker daemon is not running (or is not accessible)"
"$DOCKER_BIN" compose version >/dev/null 2>&1 || fail "Docker Compose v2 is required (docker compose version failed)"

mkdir -p "$INSTALL_DIR" || fail "cannot create install directory '$INSTALL_DIR'"
chmod 700 "$INSTALL_DIR" || true

ENV_FILE="$INSTALL_DIR/.env"
STATE_FILE="$INSTALL_DIR/.depyard-state"
COMPOSE_FILE="$INSTALL_DIR/docker-compose.yml"

if [[ -f "$INSTALL_DIR/docker-compose.release.yml" ]]; then
  warn "Found legacy docker-compose.release.yml in $INSTALL_DIR"
  warn "The installer now manages docker-compose.yml itself; the old file is ignored and can be removed."
fi

env_value() {
  local key="$1" file="$2" line
  [[ -f "$file" ]] || return 0
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    if [[ "$line" == "$key="* ]]; then
      printf '%s' "${line#*=}"
      return 0
    fi
  done < "$file"
}

generate_password() {
  local value=''
  if command -v openssl >/dev/null 2>&1; then
    value="$(openssl rand -base64 48 | tr -dc 'A-Za-z0-9_-' | cut -c1-32)"
  else
    value="$(head -c 96 /dev/urandom | base64 | tr -dc 'A-Za-z0-9_-' | cut -c1-32)"
  fi
  [[ ${#value} -eq 32 ]] || fail "could not generate a strong initial password"
  printf '%s' "$value"
}

compose() {
  "$DOCKER_BIN" compose \
    --project-name "$PROJECT_NAME" \
    --project-directory "$INSTALL_DIR" \
    --env-file "$ENV_FILE" \
    -f "$COMPOSE_FILE" \
    "$@"
}

uninstall() {
  local confirmation volume

  if [[ -f "$COMPOSE_FILE" ]]; then
    info "Stopping Depyard containers"
    compose down --remove-orphans || warn "could not remove all Depyard containers"
  else
    warn "No managed Compose file at $COMPOSE_FILE; skipping container removal"
  fi

  if [[ "$PURGE" -eq 0 ]]; then
    ok "Depyard stopped. Configuration and data volumes were kept."
    printf '\n  To delete data and configuration too: bash install.sh --uninstall --purge\n\n'
    return 0
  fi

  printf '\n%s%s%s\n' "$YELLOW" "This permanently deletes:" "$RESET"
  printf '  - the Compose project %q and all its containers/networks\n' "$PROJECT_NAME"
  printf '  - MongoDB volume %s (users, tokens, registry metadata)\n' "${PROJECT_NAME}_mongo_data"
  printf '  - blobs volume %s (all stored artifacts)\n' "${PROJECT_NAME}_registry_blobs"
  printf '  - %s, %s, and %s\n\n' "$ENV_FILE" "$STATE_FILE" "$COMPOSE_FILE"

  if [[ "$ASSUME_YES" -eq 0 ]]; then
    read -r -p "Type 'yes' to continue: " confirmation
    [[ "$confirmation" == "yes" ]] || fail "uninstall aborted"
  fi

  if [[ -f "$COMPOSE_FILE" ]]; then
    info "Removing containers and volumes"
    compose down --volumes --remove-orphans || warn "could not remove all Depyard volumes"
  else
    info "Removing Depyard volumes"
    while IFS= read -r volume; do
      [[ -n "$volume" ]] || continue
      "$DOCKER_BIN" volume rm "$volume" >/dev/null 2>&1 || warn "could not remove volume $volume"
    done < <("$DOCKER_BIN" volume ls --format '{{.Name}}' 2>/dev/null | grep -E "^${PROJECT_NAME}[_-](mongo_data|registry_blobs)$" || true)
  fi

  rm -f "$ENV_FILE" "$STATE_FILE" "$COMPOSE_FILE"
  rmdir "$INSTALL_DIR" 2>/dev/null || true
  ok "Depyard uninstalled; containers, volumes and configuration were removed."
}

# Decide whether this is a fresh install, an installer-managed install, or an
# older install whose volumes exist but whose state file is missing.
FRESH=0
LEGACY_PROJECT=''
if [[ -f "$STATE_FILE" ]]; then
  # shellcheck disable=SC1090
  source "$STATE_FILE"
  [[ "${DEPWARD_STATE_VERSION:-}" == "1" ]] || fail "unsupported installer state in $STATE_FILE"
  PROJECT_NAME="${DEPWARD_PROJECT_NAME:-depyard}"
  INSTALLED_VERSION="${DEPWARD_VERSION:-}"
elif [[ -f "$ENV_FILE" ]]; then
  PROJECT_NAME="$(env_value COMPOSE_PROJECT_NAME "$ENV_FILE")"
  PROJECT_NAME="${PROJECT_NAME:-depyard}"
  INSTALLED_VERSION="$(env_value DEPWARD_VERSION "$ENV_FILE")"
else
  volume_names=()
  while IFS= read -r volume; do
    volume_names+=("$volume")
  done < <("$DOCKER_BIN" volume ls --format '{{.Name}}' 2>/dev/null | grep -E '(^|_)mongo_data$' || true)
  if [[ ${#volume_names[@]} -eq 0 ]]; then
    FRESH=1
    PROJECT_NAME="depyard"
  else
    projects=()
    seen=''
    for volume in "${volume_names[@]}"; do
      candidate="${volume%_mongo_data}"
      [[ -n "$candidate" ]] || continue
      if [[ "$seen" != *"$candidate"* ]]; then
        projects+=("$candidate")
        seen="${seen} ${candidate} "
      fi
    done
    if [[ ${#projects[@]} -eq 0 ]]; then
      fail "existing Depyard volumes were found but no Compose project could be inferred. Set COMPOSE_PROJECT_NAME in $ENV_FILE and re-run."
    fi
    if [[ ${#projects[@]} -ne 1 ]]; then
      fail "existing Depyard volumes were found for projects [${projects[*]}]. Set COMPOSE_PROJECT_NAME in $ENV_FILE and re-run."
    fi
    LEGACY_PROJECT="${projects[0]}"
    PROJECT_NAME="$LEGACY_PROJECT"
    warn "Existing Depyard volumes detected for project '$PROJECT_NAME'."
    warn "No new password will be generated; the existing administrator is preserved."
  fi
fi

[[ "$PROJECT_NAME" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || \
  fail "invalid Compose project name '$PROJECT_NAME' (use lowercase letters, digits, '-' or '_')"

if [[ "$UPGRADE" -eq 1 && -z "$INSTALLED_VERSION" && ! -f "$ENV_FILE" && ! -f "$COMPOSE_FILE" && -z "$LEGACY_PROJECT" ]]; then
  fail "no existing installation found in $INSTALL_DIR; run without --upgrade to install"
fi

if [[ "$UNINSTALL" -eq 1 ]]; then
  uninstall
  exit 0
fi

# Read back any values already stored in .env so a re-run never rotates the password.
existing_username="$(env_value ADMIN_USERNAME "$ENV_FILE")"
existing_email="$(env_value ADMIN_EMAIL "$ENV_FILE")"
existing_password="$(env_value ADMIN_PASSWORD "$ENV_FILE")"
existing_port="$(env_value HTTP_PORT "$ENV_FILE")"

if [[ "$ADMIN_USERNAME_EXPLICIT" -eq 0 && -n "$existing_username" ]]; then ADMIN_USERNAME="$existing_username"; fi
if [[ "$ADMIN_EMAIL_EXPLICIT" -eq 0 && -n "$existing_email" ]]; then ADMIN_EMAIL="$existing_email"; fi
if [[ "$HTTP_PORT_EXPLICIT" -eq 0 && -n "$existing_port" ]]; then HTTP_PORT="$existing_port"; fi

# Explicit environment settings override saved settings. An explicitly empty
# certificate/key pair turns TLS off; never silently discard TLS on upgrade.
BIND_HOST="${BIND_HOST-$(env_value BIND_HOST "$ENV_FILE")}"
BIND_PORT="${BIND_PORT-$(env_value BIND_PORT "$ENV_FILE")}"
TLS_CERT_DIR="${TLS_CERT_DIR-$(env_value TLS_CERT_DIR "$ENV_FILE")}"
TLS_CERT_FILE="${TLS_CERT_FILE-$(env_value TLS_CERT_FILE "$ENV_FILE")}"
TLS_KEY_FILE="${TLS_KEY_FILE-$(env_value TLS_KEY_FILE "$ENV_FILE")}"
PUBLIC_BASE_URL="${PUBLIC_BASE_URL-$(env_value PUBLIC_BASE_URL "$ENV_FILE")}"
BIND_HOST="${BIND_HOST:-0.0.0.0}"
TLS_CERT_DIR="${TLS_CERT_DIR:-./certs}"
port="${BIND_PORT:-$HTTP_PORT}"
[[ "$port" =~ ^[0-9]{1,5}$ ]] && (( 10#$port > 0 && 10#$port <= 65535 )) || fail "invalid bind port"
[[ "$BIND_HOST" =~ ^[a-zA-Z0-9:.%-]+$ ]] || fail "invalid bind host"
for path in "$TLS_CERT_DIR" "$TLS_CERT_FILE" "$TLS_KEY_FILE"; do
  [[ "$path" =~ ^[a-zA-Z0-9_./-]*$ ]] || fail "invalid TLS path"
done
[[ -n "$TLS_CERT_FILE" && -n "$TLS_KEY_FILE" || -z "$TLS_CERT_FILE" && -z "$TLS_KEY_FILE" ]] || fail "set TLS_CERT_FILE and TLS_KEY_FILE together"
public_url_re='^https?://[][a-zA-Z0-9.:/_-]+$'
[[ -z "$PUBLIC_BASE_URL" || "$PUBLIC_BASE_URL" =~ $public_url_re ]] || fail "invalid public base URL"
scheme=http
[[ -z "$TLS_CERT_FILE" ]] || scheme=https
host="$BIND_HOST"
case "$host" in 0.0.0.0|::) host=localhost ;; *:*) host="[$host]" ;; esac
web_url="${PUBLIC_BASE_URL:-$scheme://$host:$port}"

PASSWORD_SOURCE=''
if [[ -n "$ADMIN_PASSWORD" ]]; then
  PASSWORD_SOURCE='environment'
elif [[ -n "$existing_password" ]]; then
  ADMIN_PASSWORD="$existing_password"
  PASSWORD_SOURCE='envfile'
elif [[ "$FRESH" -eq 1 ]]; then
  ADMIN_PASSWORD="$(generate_password)"
  PASSWORD_SOURCE='generated'
else
  ADMIN_PASSWORD=''
  PASSWORD_SOURCE='existing'
fi

if [[ -n "$ADMIN_PASSWORD" ]] && [[ ${#ADMIN_PASSWORD} -lt 12 ]]; then
  fail "stored ADMIN_PASSWORD is shorter than 12 characters; set a new one before re-running"
fi

if [[ "$MODE" == "offline" && "$VERSION" == "latest" ]]; then
  fail "offline installs need an explicit version: bash install.sh --offline --version vX.Y.Z"
fi

write_env_file() {
  local tmp="${ENV_FILE}.tmp.$$" old_umask
  old_umask="$(umask)"
  umask 077
  cat > "$tmp" <<EOF
# Generated by the Depyard installer. Secrets are stored here with 0600 permissions.
# Edit this file to change settings; the managed Compose file is regenerated on each run.
COMPOSE_PROJECT_NAME=${PROJECT_NAME}
HTTP_PORT=${HTTP_PORT}
BIND_HOST=${BIND_HOST}
BIND_PORT=${BIND_PORT}
TLS_CERT_DIR=${TLS_CERT_DIR}
TLS_CERT_FILE=${TLS_CERT_FILE}
TLS_KEY_FILE=${TLS_KEY_FILE}
PUBLIC_BASE_URL=${PUBLIC_BASE_URL}
IMAGE_PREFIX=${IMAGE_PREFIX}
DEPWARD_VERSION=${VERSION}
ADMIN_USERNAME=${ADMIN_USERNAME}
ADMIN_EMAIL=${ADMIN_EMAIL}
ADMIN_PASSWORD=${ADMIN_PASSWORD}
EOF
  chmod 600 "$tmp"
  mv -f "$tmp" "$ENV_FILE"
  umask "$old_umask"
}

write_compose_file() {
  local tmp="${COMPOSE_FILE}.tmp.$$"
  cat > "$tmp" <<EOF
# Generated by the Depyard installer for ${VERSION}. Do not edit.
# Change settings in .env and re-run install.sh instead.
services:
  mongodb:
    image: mongo:7
    restart: unless-stopped
    volumes:
      - mongo_data:/data/db
    healthcheck:
      test: ["CMD", "mongosh", "--quiet", "--eval", "db.adminCommand('ping').ok"]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 10s

  backend:
    image: ${IMAGE_PREFIX}-backend:${VERSION}
    restart: unless-stopped
    depends_on:
      mongodb:
        condition: service_healthy
    environment:
      LISTEN_ADDR: 0.0.0.0:8080
      MONGODB_URI: mongodb://mongodb:27017
      MONGODB_DATABASE: mirror_registry
      DATA_DIR: /data
      PUBLIC_BASE_URL: \${PUBLIC_BASE_URL:-}
      ENABLE_ROOT_COMPAT_ROUTE: "true"
      DEFAULT_REGISTRY_SLUG: pub-public
      DEFAULT_REGISTRY_NAME: Pub Public Mirror
      DEFAULT_REGISTRY_MODE: hybrid
      DEFAULT_UPSTREAM_URL: https://pub.dev
      SEED_FLUTTER_STORAGE: "true"
      DEFAULT_FLUTTER_SLUG: flutter-storage
      DEFAULT_FLUTTER_UPSTREAM_URL: https://storage.googleapis.com
      SEED_GO_PROXY: "true"
      DEFAULT_GO_SLUG: go-proxy
      DEFAULT_GO_UPSTREAM_URL: https://proxy.golang.org
      SEED_PYPI: "true"
      DEFAULT_PYPI_SLUG: python-packages
      DEFAULT_PYPI_MODE: hybrid
      DEFAULT_PYPI_UPSTREAM_URL: https://pypi.org
      SEED_NPM: "true"
      DEFAULT_NPM_SLUG: npm-packages
      DEFAULT_NPM_MODE: hybrid
      DEFAULT_NPM_UPSTREAM_URL: https://registry.npmjs.org
      ADMIN_USERNAME: \${ADMIN_USERNAME:-admin}
      ADMIN_EMAIL: \${ADMIN_EMAIL:-admin@example.com}
      ADMIN_PASSWORD: \${ADMIN_PASSWORD:-}
    volumes:
      - registry_blobs:/data
    expose:
      - "8080"
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:8080/healthz > /dev/null || exit 1"]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 5s

  nginx:
    image: ${IMAGE_PREFIX}-nginx:${VERSION}
    restart: unless-stopped
    depends_on:
      backend:
        condition: service_healthy
    environment:
      BACKEND_UPSTREAM: backend:8080
      TLS_CERT_FILE: \${TLS_CERT_FILE:-}
      TLS_KEY_FILE: \${TLS_KEY_FILE:-}
    volumes:
      - \${TLS_CERT_DIR:-./certs}:/run/depyard/tls:ro
    ports:
      - target: 80
        published: "\${BIND_PORT:-\${HTTP_PORT:-80}}"
        host_ip: "\${BIND_HOST:-0.0.0.0}"
        protocol: tcp

volumes:
  mongo_data:
  registry_blobs:
EOF
  mv -f "$tmp" "$COMPOSE_FILE"
}

write_state_file() {
  local tmp="${STATE_FILE}.tmp.$$" installed_at
  installed_at="$(env_value DEPWARD_INSTALLED_AT "$STATE_FILE")"
  [[ -n "$installed_at" ]] || installed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  cat > "$tmp" <<EOF
DEPWARD_STATE_VERSION=1
DEPWARD_VERSION=${VERSION}
DEPWARD_PROJECT_NAME=${PROJECT_NAME}
DEPWARD_HTTP_PORT=${HTTP_PORT}
DEPWARD_INSTALLED_AT=${installed_at}
DEPWARD_PASSWORD_SOURCE=${PASSWORD_SOURCE}
EOF
  chmod 600 "$tmp"
  mv -f "$tmp" "$STATE_FILE"
}

verify_archive() {
  local archive="$1" sums="$2" expected actual
  [[ -f "$sums" ]] || { warn "SHA256SUMS.txt not found beside install.sh; skipping checksum verification"; return 0; }
  expected="$(awk -v name="$(basename "$archive")" '$0 ~ "  "name"$" { print $1 }' "$sums")"
  [[ -n "$expected" ]] || fail "no checksum for $(basename "$archive") in $sums"
  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$archive" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$archive" | awk '{print $1}')"
  else
    fail "sha256sum or shasum is required to verify the offline archive"
  fi
  [[ "$actual" == "$expected" ]] || fail "checksum mismatch for $(basename "$archive")"
}

print_header() {
  printf '\n%s%s%s\n' "$BOLD" "Depyard ${VERSION}" "$RESET"
  printf '%s\n' "Install directory: ${INSTALL_DIR}"
  printf '%s\n' "Compose project:   ${PROJECT_NAME}"
  printf '\n'
}

print_ready() {
  printf '%sDepyard %s is ready.%s\n' "$GREEN" "$VERSION" "$RESET"
  printf '\n'
  printf '  %-11s %s\n' 'Web UI:' "$web_url"
  printf '  %-11s %s\n' 'Username:' "$ADMIN_USERNAME"
  if [[ "$PASSWORD_SOURCE" == "generated" ]]; then
    printf '  %-11s %s\n' 'Password:' "$ADMIN_PASSWORD"
  fi
  printf '  %-11s %s\n' 'Config:' "$ENV_FILE"
  printf '\n'
  printf 'Next steps:\n'
  printf '  1. Sign in and change your password: My Account -> Profile\n'
  printf '  2. Enable the registries you need: Admin -> Registries\n'
  printf '     (The first-install registry catalogue is disabled by default.)\n'
  if [[ "$PASSWORD_SOURCE" == "generated" ]]; then
    printf '\n  The password was shown once and saved in %s\n' "$ENV_FILE"
  fi
  printf '\n'
}

print_existing() {
  printf '%sDepyard %s is ready.%s\n' "$GREEN" "$VERSION" "$RESET"
  printf '\n'
  printf '  %-11s %s\n' 'Web UI:' "$web_url"
  printf '  %-11s %s\n' 'Config:' "$ENV_FILE"
  printf '\n'
  if [[ "$PASSWORD_SOURCE" == "environment" ]]; then
    printf '  Using ADMIN_PASSWORD from the environment (not displayed).\n\n'
  else
    printf '  Password is stored in %s and is not displayed on re-runs.\n\n' "$ENV_FILE"
  fi
}

print_header

if [[ "$FRESH" -eq 1 ]]; then
  info "Preparing a fresh Depyard installation"
elif [[ -n "$LEGACY_PROJECT" ]]; then
  info "Adopting existing Depyard volumes"
elif [[ "$UPGRADE" -eq 1 ]]; then
  if [[ -n "$INSTALLED_VERSION" && "$INSTALLED_VERSION" != "$VERSION" ]]; then
    info "Upgrading Depyard ${INSTALLED_VERSION} -> ${VERSION}"
  elif [[ -n "$INSTALLED_VERSION" ]]; then
    info "Depyard ${VERSION} is already installed; re-applying"
  else
    info "Upgrading legacy Depyard installation to ${VERSION}"
  fi
else
  info "Re-applying Depyard ${VERSION}"
fi

write_env_file
write_compose_file
ok "Wrote configuration and managed Compose file"
if [[ "$UPGRADE" -eq 1 ]]; then
  ok "Preserving .env, installer state and data volumes"
fi

if [[ "$MODE" == "offline" ]]; then
  case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) fail "unsupported architecture '$(uname -m)' for offline install" ;;
  esac
  archive="depyard-images-${VERSION}-linux-${ARCH}.tar.gz"
  archive_path="${SCRIPT_DIR}/${archive}"
  [[ -f "$archive_path" ]] || fail "expected ${archive} beside install.sh, but it was not found"
  verify_archive "$archive_path" "${SCRIPT_DIR}/SHA256SUMS.txt"
  info "Loading images from ${archive}"
  "$DOCKER_BIN" load -i "$archive_path" >/dev/null
  ok "Images loaded"
  info "Starting Depyard"
  compose up -d --pull never --wait
else
  info "Pulling images for ${VERSION}"
  compose pull
  info "Starting Depyard"
  compose up -d --wait
fi

write_state_file
ok "Installer state saved"

if [[ "$FRESH" -eq 1 ]]; then
  print_ready
else
  print_existing
fi
