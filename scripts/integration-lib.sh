#!/usr/bin/env bash
# Shared isolation and readiness for host integration checks. Source this file.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export PGHOST=${PGHOST:-127.0.0.1} PGPORT=${PGPORT:-5432} PGUSER=${PGUSER:-postgres}
fail() { echo "FAILED: $*" >&2; exit 1; }
step() { printf '\n=== %s\n' "$*"; }
urlencode() {
  local LC_ALL=C value=$1 out= char hex i
  for ((i = 0; i < ${#value}; i++)); do
    char=${value:i:1}
    case "$char" in
      [a-zA-Z0-9.~_-]) out+="$char" ;;
      *)
        printf -v hex '%02X' "'$char"
        out+="%$hex"
        ;;
    esac
  done
  printf '%s' "$out"
}
wait_for() {
  local end=$((SECONDS + $1)); shift
  until "$@"; do
    (( SECONDS < end )) || fail "timed out: $*"
    sleep 0.2
  done
}
integration_init() {
  local name=$1 port=$2
  RUN=$(mktemp -d "${TMPDIR:-/tmp}/gitman-$name.XXXXXXXX")
  DATABASE="gitman_${name}_$(basename "$RUN" | tr -cd 'a-zA-Z0-9' | tr '[:upper:]' '[:lower:]')_test"
  [[ "$DATABASE" =~ ^gitman_[a-z0-9_]+_test$ ]] || fail 'invalid test database name'
  DATA="$RUN/data"; W="$RUN/work"; BIN="$RUN/gitman"; PORT=$port; BASE="http://127.0.0.1:$PORT"
  mkdir -p "$DATA" "$W"
  local userinfo database_url
  userinfo=$(urlencode "$PGUSER")
  if [[ -n ${PGPASSWORD:-} ]]; then
    userinfo+=":$(urlencode "$PGPASSWORD")"
  fi
  database_url="postgresql://${userinfo}@/${DATABASE}?host=$(urlencode "$PGHOST")&port=$(urlencode "$PGPORT")&sslmode=disable"
  export GITMAN_DATABASE_URL="$database_url"
  export GITMAN_DATA_DIR="$DATA" GITMAN_PUBLIC_URL="$BASE" GITMAN_PORT="$PORT"
  export GITMAN_SECRET_KEY="$(openssl rand -base64 32)"
  trap integration_cleanup EXIT
  # Never drop an existing database or reuse an existing data directory.
  createdb "$DATABASE"
  DATABASE_CREATED=1
  step 'build'
  (cd "$ROOT" && go build -o "$BIN" ./cmd/gitman)
  if curl -sf --max-time 1 "$BASE/healthz" >/dev/null; then fail "port $PORT is occupied"; fi
  "$BIN" web >"$RUN/web.log" 2>&1 & echo $! >"$RUN/web.pid"
  wait_for 30 curl -sf --max-time 1 "$BASE/readyz" -o /dev/null
  echo "Artifacts: $RUN"
}
integration_cleanup() {
  local result=$?
  trap - EXIT
  for file in "$RUN/worker.pid" "$RUN/web.pid"; do
    if [[ -f "$file" ]]; then
      local pid; pid=$(cat "$file")
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  if [[ ${DATABASE_CREATED:-0} == 1 ]]; then dropdb "$DATABASE" || result=1; fi
  # Keep logs/screenshots on success and failure for inspection, never credentials.
  rm -f "$RUN/admin.txt" "$RUN/cookies"
  if [[ -n ${GITMAN_INTEGRATION_ARTIFACTS:-} ]]; then
    mkdir -p "$GITMAN_INTEGRATION_ARTIFACTS"
    local evidence="$GITMAN_INTEGRATION_ARTIFACTS/$(basename "$RUN")"
    mkdir -p "$evidence"
    # Publish evidence only, not binaries, database snapshots or workspaces.
    for file in "$RUN"/*.log "$RUN"/push-*.txt "$RUN"/worker-cleanup.txt; do
      [[ ! -f "$file" ]] || cp "$file" "$evidence/"
    done
    [[ ! -d "$RUN/screenshots" ]] || cp -R "$RUN/screenshots" "$evidence/"
  fi
  exit "$result"
}
start_worker() {
  "$BIN" worker >>"$RUN/worker.log" 2>&1 & echo $! >"$RUN/worker.pid"
  wait_for 30 worker_ready
}
worker_ready() { [[ $(psql -d "$DATABASE" -tAc "SELECT count(*) FROM workers WHERE ready AND stopped_at IS NULL") -gt 0 ]]; }
bootstrap_admin() {
  "$BIN" admin person add --admin darius >"$RUN/admin.txt"
  local bootstrap; bootstrap=$(awk '/^Password:/ {print $2}' "$RUN/admin.txt")
  PASSWORD='Integration-password-123!'; JAR="$RUN/cookies"
  local code
  code=$(curl -sS -c "$JAR" -o /dev/null -w '%{http_code}' -H 'Sec-Fetch-Site: same-origin' --data-urlencode username=darius --data-urlencode "password=$bootstrap" "$BASE/login")
  [[ "$code" == 303 ]] || fail "bootstrap login: $code"
  code=$(curl -sS -b "$JAR" -c "$JAR" -o /dev/null -w '%{http_code}' -H 'Sec-Fetch-Site: same-origin' --data-urlencode "current_password=$bootstrap" --data-urlencode "new_password=$PASSWORD" --data-urlencode "confirm_password=$PASSWORD" "$BASE/me/password")
  [[ "$code" == 303 ]] || fail "bootstrap password change: $code"
  rm -f "$RUN/admin.txt"
}
settled() {
  local end=$((SECONDS + 120)) status
  while ((SECONDS < end)); do
    status=$(psql -d "$DATABASE" -tAc "SELECT r.status FROM runs r JOIN repos p ON p.id=r.repo_id WHERE p.name='$1' AND r.number=$2")
    case "$status" in passed|failed|cancelled) return;; esac
    sleep 0.2
  done
  fail "run $1 #$2 did not finish"
}
