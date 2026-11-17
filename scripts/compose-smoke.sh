#!/usr/bin/env bash
# Isolated installation rehearsal using the production Compose definition.
# It never reads or writes the caller's .env or production data directory.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
command -v docker >/dev/null || { echo 'docker is required' >&2; exit 1; }
command -v curl >/dev/null || { echo 'curl is required' >&2; exit 1; }
command -v openssl >/dev/null || { echo 'openssl is required' >&2; exit 1; }
run=$(mktemp -d "${TMPDIR:-/tmp}/gitman-compose-smoke.XXXXXXXX")
project="gitman_smoke_$(basename "$run" | tr -cd 'a-z0-9' | cut -c1-12)"
image="gitman:compose-smoke-${project}"
chmod 711 "$run"
mkdir -p "$run/data"
chmod 700 "$run/data"
web_port=${GITMAN_COMPOSE_SMOKE_HTTP_PORT:-18085}
db_port=${GITMAN_COMPOSE_SMOKE_DB_PORT:-15433}
[[ "$web_port" =~ ^[0-9]+$ && "$db_port" =~ ^[0-9]+$ ]] || { echo 'invalid smoke ports' >&2; exit 1; }
cat > "$run/.env" <<ENV
GITMAN_IMAGE=$image
GITMAN_DATA_DIR=$run/data
GITMAN_DATABASE_PASSWORD=composeSmokeOnly123
GITMAN_SECRET_KEY=$(openssl rand -base64 32)
GITMAN_PUBLIC_URL=http://127.0.0.1:$web_port
GITMAN_HTTP_PORT=$web_port
GITMAN_DATABASE_PORT=$db_port
GITMAN_BIND_ADDRESS=127.0.0.1
GITMAN_DISK_RESERVE_GIB=1
ENV
chmod 600 "$run/.env"
# Copy Compose outside the repository so its relative env_file: .env resolves
# only to our temporary credentials, never to a user's real .env.
cp "$ROOT/compose.yaml" "$run/compose.yaml"
cat > "$run/compose-override.yml" <<ENV
services:
  web:
    build:
      context: $ROOT
ENV
compose() {
  docker compose -p "$project" -f "$run/compose.yaml" -f "$run/compose-override.yml" --env-file "$run/.env" "$@"
}
cleanup() {
  local status=$?
  trap - EXIT
  if [[ "$status" -ne 0 ]]; then
    compose ps >&2 || true
    compose logs --tail 80 web db worker >&2 || true
  fi
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  docker image rm "$image" >/dev/null 2>&1 || true
  chmod u+rwx "$run" 2>/dev/null || true
  if ! rm -rf -- "$run" 2>/dev/null; then
    # init-data and the worker write files as container UIDs. The GitHub
    # runner has noninteractive sudo; local installs may need manual cleanup.
    if command -v sudo >/dev/null && sudo -n rm -rf -- "$run"; then
      :
    else
      echo "Unable to remove temporary Compose data: $run" >&2
      status=1
    fi
  fi
  exit "$status"
}
trap cleanup EXIT
compose config --quiet
compose up -d --build --wait --wait-timeout 180
curl --fail --silent --show-error --max-time 10 "http://127.0.0.1:${web_port}/readyz" >/dev/null
compose exec -T web gitman version
# The installed web service and worker must share an initialized schema.
compose exec -T web gitman admin migrate
# Keep an independent record in the database; ensure container replacement
# still sees the same storage. The test uses a disposable named volume.
compose exec -T db psql -U gitman -d gitman -v ON_ERROR_STOP=1 -c \
  "CREATE TABLE IF NOT EXISTS compose_smoke_persistence (id integer PRIMARY KEY); INSERT INTO compose_smoke_persistence VALUES (1) ON CONFLICT DO NOTHING;" >/dev/null
compose up -d --force-recreate --wait --wait-timeout 180
[[ $(compose exec -T db psql -U gitman -d gitman -Atc 'SELECT count(*) FROM compose_smoke_persistence') == 1 ]] || {
  echo 'PostgreSQL data did not survive Compose container replacement' >&2
  exit 1
}
compose ps
printf 'Production Compose smoke installation passed (isolated project %s)\n' "$project"
