#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
export HTTP_PORT="${HTTP_PORT:-8080}"
compose=(docker compose -f docker-compose.release.yml)
docker info >/dev/null
docker compose version >/dev/null
case "${1:-online}" in
  online)
    "${compose[@]}" pull
    "${compose[@]}" up -d --wait
    ;;
  offline)
    case "$(docker info --format '{{.Architecture}}')" in
      x86_64|amd64) arch=amd64 ;;
      aarch64|arm64) arch=arm64 ;;
      *) echo "Unsupported Docker architecture" >&2; exit 1 ;;
    esac
    shopt -s nullglob
    archives=(depyard-images-*-linux-"$arch".tar.gz)
    if [[ ${#archives[@]} != 1 ]]; then
      echo "Place exactly one matching $arch release image archive beside this script." >&2
      exit 1
    fi
    if [[ -f SHA256SUMS.txt ]]; then
      sha256sum --ignore-missing -c SHA256SUMS.txt
    fi
    docker load -i "${archives[0]}"
    "${compose[@]}" up -d --pull never --wait
    ;;
  *) echo "Usage: bash install.sh [online|offline]" >&2; exit 1 ;;
esac
echo "Depyard is running at http://localhost:$HTTP_PORT"
echo "Initial admin credentials (only on first boot):"
"${compose[@]}" logs --no-color backend | grep 'generated for the initial admin' || true
