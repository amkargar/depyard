#!/usr/bin/env bash
# Smoke test for the self-contained installer using a stubbed Docker CLI.
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_SCRIPT="${INSTALL_SCRIPT:-$ROOT_DIR/install.sh}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

DOCKER_STUB="$WORK/docker"
DOCKER_LOG="$WORK/docker.log"
export DOCKER_LOG
export VOLUMES_OUT=''

cat > "$DOCKER_STUB" <<'STUB'
#!/usr/bin/env bash
set -e
echo "$*" >> "$DOCKER_LOG"
case "$1" in
  info) exit 0 ;;
  volume) echo "$VOLUMES_OUT"; exit 0 ;;
  load) exit 0 ;;
  compose)
    shift
    while [[ $# -gt 0 ]]; do
      case "$1" in
        version|pull|up|down) exit 0 ;;
      esac
      shift
    done
    exit 0 ;;
esac
exit 0
STUB
chmod +x "$DOCKER_STUB"

run_installer() {
  local dir="$1"; shift
  DOCKER_BIN="$DOCKER_STUB" DEPWARD_INSTALL_DIR="$dir" \
    bash "$INSTALL_SCRIPT" "$@"
}

check() {
  printf '  ok  %s\n' "$1"
}

# Fresh online install generates a password and writes managed files.
fresh="$WORK/fresh"
out="$(run_installer "$fresh" --version v1.4.0 --http-port 9099)"
[[ "$out" == *'Depyard v1.4.0 is ready.'* ]]
[[ "$out" == *'http://localhost:9099'* ]]
[[ "$out" == *'Password:'* ]]
password="$(awk '/Password:/{print $2}' <<< "$out")"
[[ ${#password} -eq 32 ]]
[[ "$(stat -c '%a' "$fresh/.env" 2>/dev/null || stat -f '%Lp' "$fresh/.env")" == 600 ]]
grep -q '^COMPOSE_PROJECT_NAME=depyard$' "$fresh/.env"
grep -q '^HTTP_PORT=9099$' "$fresh/.env"
grep -q "^ADMIN_PASSWORD=$password$" "$fresh/.env"
grep -q 'image: ghcr.io/amkargar/depyard-backend:v1.4.0' "$fresh/docker-compose.yml"
grep -q 'ADMIN_PASSWORD: ${ADMIN_PASSWORD:-}' "$fresh/docker-compose.yml"
! grep -q "$password" "$fresh/docker-compose.yml"
[[ -f "$fresh/.depyard-state" ]]
cp "$fresh/.env" "$WORK/first.env"
check 'fresh online install'

# Re-runs are idempotent and never print the stored password.
out="$(run_installer "$fresh" --version v1.4.0)"
[[ "$out" != *'Password:'* ]]
diff -q "$WORK/first.env" "$fresh/.env" >/dev/null
grep -q '^HTTP_PORT=9099$' "$fresh/.env"
check 'idempotent re-run'

# Explicit flags override stored values.
out="$(run_installer "$fresh" --version v1.4.0 --http-port 7070)"
[[ "$out" == *'http://localhost:7070'* ]]
grep -q '^HTTP_PORT=7070$' "$fresh/.env"
check 'explicit port override'

# Offline install uses the archive beside the script and verifies its checksum.
offline="$WORK/offline"
mkdir -p "$offline"
cp "$INSTALL_SCRIPT" "$offline/install.sh"
archive="depyard-images-v1.4.0-linux-amd64.tar.gz"
printf 'test-archive-content' > "$offline/$archive"
sha="$(sha256sum "$offline/$archive" | awk '{print $1}')"
printf '%s  %s\n' "$sha" "$archive" > "$offline/SHA256SUMS.txt"
out="$(DOCKER_BIN="$DOCKER_STUB" DEPWARD_INSTALL_DIR="$offline/install" \
  bash "$offline/install.sh" --offline --version v1.4.0)"
[[ "$out" == *'Images loaded'* ]]
grep -q '^load -i ' "$DOCKER_LOG"
check 'offline install and checksum verification'

# Offline installs cannot guess an archive name from the "latest" tag.
if DOCKER_BIN="$DOCKER_STUB" DEPWARD_INSTALL_DIR="$offline/other" \
   bash "$offline/install.sh" --offline --version latest >"$WORK/latest.out" 2>&1; then
  echo 'offline install with "latest" unexpectedly succeeded' >&2
  exit 1
fi
grep -q 'offline installs need an explicit version' "$WORK/latest.out"
check 'offline version guard'

# Legacy volumes are adopted without rotating the administrator password.
export VOLUMES_OUT='oldproj_mongo_data'
legacy="$WORK/legacy"
out="$(run_installer "$legacy" --version v1.4.0)"
grep -q '^COMPOSE_PROJECT_NAME=oldproj$' "$legacy/.env"
[[ "$out" != *'Password:'* ]]
grep -q '^ADMIN_PASSWORD=$' "$legacy/.env"
check 'legacy volume adoption'

# Uninstall stops containers but keeps configuration and volumes by default.
uninstall_dir="$WORK/uninstall"
run_installer "$uninstall_dir" --version v1.4.0 >/dev/null
out="$(run_installer "$uninstall_dir" --version v1.4.0 --uninstall)"
[[ "$out" == *'Configuration and data volumes were kept'* ]]
[[ -f "$uninstall_dir/.env" ]]
[[ -f "$uninstall_dir/docker-compose.yml" ]]
[[ -f "$uninstall_dir/.depyard-state" ]]
grep -q 'down --remove-orphans' "$DOCKER_LOG"
check 'uninstall keeps config and volumes'

# Purge removes volumes and configuration without prompting when --yes is given.
out="$(run_installer "$uninstall_dir" --version v1.4.0 --uninstall --purge --yes)"
[[ "$out" == *'uninstalled'* ]]
grep -q 'down --volumes --remove-orphans' "$DOCKER_LOG"
[[ ! -e "$uninstall_dir/.env" ]]
[[ ! -e "$uninstall_dir/docker-compose.yml" ]]
[[ ! -e "$uninstall_dir/.depyard-state" ]]
check 'purge removes config and volumes'

# --purge without --uninstall is rejected.
if run_installer "$uninstall_dir" --version v1.4.0 --purge >"$WORK/purge.out" 2>&1; then
  echo '--purge without --uninstall unexpectedly succeeded' >&2
  exit 1
fi
grep -q -- '--purge only applies with --uninstall' "$WORK/purge.out"
check 'purge guard'

printf 'Installer smoke tests passed.\n'
