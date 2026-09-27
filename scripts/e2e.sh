#!/usr/bin/env bash
# A real end-to-end run of Gitman: builds the binary, starts web and a
# worker against a real PostgreSQL database, and pushes real Git commits
# over HTTP to drive real pipeline runs in real Docker containers.
#
# Requires, all reachable from this shell: Go, git, curl, psql, and a
# Docker daemon with alpine:3.20 already pulled (Gitman never pulls
# images itself, and neither does this script). PostgreSQL is reached as
# PGUSER at PGHOST:PGPORT (postgres at 127.0.0.1:5432 by default). The
# script creates and drops a database named gitman_e2e, and serves on
# GITMAN_E2E_PORT (18080 by default). It looks only at the step
# containers of the instance it starts, so it can share a Docker host.
#
# Run from anywhere; it always operates on the repository this script
# lives in:
#   scripts/e2e.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export PGHOST=${PGHOST:-127.0.0.1} PGPORT=${PGPORT:-5432} PGUSER=${PGUSER:-postgres}
BIN="$ROOT/bin/gitman"
RUN="$ROOT/.data/e2e"
DATA="$RUN/data"
W="$RUN/work"
PORT=${GITMAN_E2E_PORT:-18080}
BASE=http://127.0.0.1:$PORT
export GITMAN_DATABASE_URL="postgres://$PGUSER@$PGHOST:$PGPORT/gitman_e2e?sslmode=disable"
export GITMAN_DATA_DIR=$DATA
export GITMAN_PUBLIC_URL=$BASE
export GITMAN_PORT=$PORT
export GITMAN_SECRET_KEY='an end-to-end secret key, at least 32 bytes'
export GITMAN_RETENTION_DAYS=90

step() { printf '\n=== %s\n' "$*"; }
fail() { echo "FAILED: $*" >&2; exit 1; }
# cleanup stops what the script started. A process may be gone already —
# the worker is killed on purpose — so a failed kill is not a failure.
cleanup() {
  for pid in "$RUN/web.pid" "$RUN/worker.pid"; do
    [ -f "$pid" ] && kill "$(cat "$pid")" 2>/dev/null || true
  done
}
trap cleanup EXIT

step "build"
go build -o "$BIN" ./cmd/gitman

step "fresh database and data directory"
psql -qc 'DROP DATABASE IF EXISTS gitman_e2e' -c 'CREATE DATABASE gitman_e2e'
rm -rf "$RUN" && mkdir -p "$DATA" "$W"

step "start web and worker"
curl -s -o /dev/null "$BASE/" && fail "something is already serving $BASE; set GITMAN_E2E_PORT"
"$BIN" web > "$RUN/web.log" 2>&1 &
echo $! > "$RUN/web.pid"
for i in $(seq 1 50); do curl -sf "$BASE/readyz" >/dev/null && break; sleep 0.2; done
curl -sf "$BASE/readyz" >/dev/null || fail "web did not become ready"
"$BIN" worker > "$RUN/worker.log" 2>&1 &
echo $! > "$RUN/worker.pid"
sleep 1
INSTANCE=$(psql -d gitman_e2e -tAc 'SELECT id FROM instance')
# steps lists this instance's running step containers.
steps() { docker ps -q --filter "label=gitman.instance=$INSTANCE" "$@"; }

step "admin, repository, write token"
"$BIN" admin person add --admin darius | tee "$RUN/admin.txt"
PASSWORD=$(awk '/^Password:/ {print $2}' "$RUN/admin.txt")
"$BIN" admin repo create --description "End-to-end" demo
TOKEN=$("$BIN" admin token create --write darius laptop | awk '/^Token:/ {print $2}')
[ -n "$TOKEN" ] || fail "no token"
"$BIN" admin rule set --run --ship demo branch main
"$BIN" admin rule set --run demo branch 'slow/*'
"$BIN" admin rule set --run demo branch 'malicious'
"$BIN" admin rule set --run demo branch 'flag-image'
"$BIN" admin rule list demo

step "sign in to the web interface"
JAR="$RUN/cookies"
code=$(curl -s -o /dev/null -w '%{http_code}' -c "$JAR" -H 'Sec-Fetch-Site: same-origin' \
  --data-urlencode username=darius --data-urlencode "password=$PASSWORD" "$BASE/login")
[ "$code" = 303 ] || fail "sign in answered $code"

step "push a repository with a pipeline"
git -C "$W" init -q -b main
git -C "$W" config user.name Darius && git -C "$W" config user.email d@example.com
cat > "$W/.gitman.yml" <<'EOF'
image: alpine:3.20
targets:
  staging:
    branch: main
steps:
  - name: build
    run: |
      echo "building $GITMAN_REPO #$GITMAN_RUN at $GITMAN_SHORT for ${GITMAN_TARGET:-no target}"
      echo "version=$GITMAN_VERSION" >> "$GITMAN_SUMMARY"
  - name: slow
    when: branch
    run: |
      if [ "${GITMAN_REF#slow/}" != "$GITMAN_REF" ]; then
        for i in $(seq 1 120); do echo "tick $i"; sleep 1; done
      fi
      echo slow step done
  - name: ship
    when: staging
    run: echo "shipping $GITMAN_VERSION to $GITMAN_TARGET"
EOF
printf '<html><script>alert(document.cookie)</script></html>\n' > "$W/index.html"
git -C "$W" add -A && git -C "$W" commit -qm "Pipeline and a page"
REMOTE="http://darius:$TOKEN@127.0.0.1:$PORT/demo.git"
git -C "$W" push "$REMOTE" main 2>&1 | tee "$RUN/push-main.txt"
grep -q "run #1 queued for branch main, shipping to staging" "$RUN/push-main.txt" || fail "push did not report run #1"

step "watch run #1 pass and ship"
for i in $(seq 1 120); do
  page=$(curl -s -b "$JAR" "$BASE/demo/runs/1")
  echo "$page" | grep -q 'status-lg is-passed' && ! echo "$page" | grep -q 'data-live-events' && break
  sleep 1
done
echo "$page" | grep -q 'status-lg is-passed' || { echo "$page" | sed -n '/run-header/,/run-actions/p'; fail "run #1 did not pass"; }
curl -s -b "$JAR" "$BASE/demo/runs/1/log?step=2" | tee "$RUN/ship.log"
grep -q "shipping .* to staging" "$RUN/ship.log" || fail "ship step output missing"
repo_page=$(curl -s -b "$JAR" "$BASE/demo")
echo "$repo_page" | grep -q '<p class="target-name">staging</p>' || fail "deployment not shown on the repository page"
curl -s -b "$JAR" "$BASE/" | grep -q '<th scope="col">staging</th>' || fail "deployment not shown on Home"
curl -s -b "$JAR" "$BASE/demo/runs/1" | grep -q '<dt>version</dt>' || fail "summary missing"

step "raw HTML is served as plain text in a sandbox"
curl -s -D - -o "$RUN/raw.html" -b "$JAR" "$BASE/demo@main/index.html?raw" | tee "$RUN/raw-headers.txt"
grep -qi '^content-type: text/plain; charset=utf-8' "$RUN/raw-headers.txt" || fail "raw HTML content type"
grep -qi "^content-security-policy: default-src 'none'; sandbox" "$RUN/raw-headers.txt" || fail "raw HTML CSP"
grep -qi '^x-content-type-options: nosniff' "$RUN/raw-headers.txt" || fail "raw nosniff"

step "a run cancelled mid-step"
git -C "$W" checkout -qb slow/one
git -C "$W" commit -q --allow-empty -m "Slow"
git -C "$W" push "$REMOTE" slow/one 2>&1 | tee "$RUN/push-slow.txt"
for i in $(seq 1 60); do
  curl -s -b "$JAR" "$BASE/demo/runs/2/log?step=1" | grep -q 'tick 3' && break
  sleep 1
done
curl -s -b "$JAR" "$BASE/demo/runs/2/log?step=1" | grep -q 'tick 3' || fail "slow step never started"
docker ps --filter "label=gitman.instance=$INSTANCE" --format '{{.Names}} {{.Status}}'
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -H 'Sec-Fetch-Site: same-origin' -X POST "$BASE/demo/runs/2/cancel")
[ "$code" = 303 ] || fail "cancel answered $code"
for i in $(seq 1 30); do
  curl -s -b "$JAR" "$BASE/demo/runs/2" | grep -q 'status-lg is-cancelled' && break
  sleep 1
done
page=$(curl -s -b "$JAR" "$BASE/demo/runs/2")
echo "$page" | grep -q 'Cancelled by darius.' || fail "run #2 not cancelled"
curl -s -b "$JAR" "$BASE/demo/runs/2/log?step=1" | tail -3
# A cancelled run's container is force-removed in the background, not
# synchronously with the page reporting "cancelled", so give it a moment.
removed=false
for i in $(seq 1 10); do
  [ -z "$(steps)" ] && { removed=true; break; }
  sleep 1
done
$removed || fail "a step container outlived the cancelled run"

step "a malicious pipeline: symlinked summary, flag-shaped image, NUL-filled output"
git -C "$W" checkout -qb malicious main
cat > "$W/.gitman.yml" <<'EOF'
image: alpine:3.20
steps:
  - name: summary-symlink
    run: |
      rm -f "$GITMAN_SUMMARY"
      ln -s /etc/hostname "$GITMAN_SUMMARY"
  - name: nul-output
    run: printf 'before\000after\000still-here\n'
EOF
git -C "$W" add -A && git -C "$W" commit -qm "Malicious pipeline: symlinked summary, NUL output"
git -C "$W" push "$REMOTE" malicious 2>&1 | tee "$RUN/push-malicious.txt"
grep -q "run #3 queued" "$RUN/push-malicious.txt" || fail "malicious pipeline's run was not queued"
for i in $(seq 1 60); do
  page=$(curl -s -b "$JAR" "$BASE/demo/runs/3")
  echo "$page" | grep -qE 'status-lg is-(passed|failed)' && break
  sleep 1
done
echo "$page" | grep -q 'status-lg is-passed' || { echo "$page" | sed -n '/run-header/,/run-actions/p'; fail "the symlink/NUL-output run did not pass cleanly"; }
echo "$page" | grep -qi '/etc/hostname\|root:' && fail "the symlinked summary leaked file content into the page"
curl -s -b "$JAR" "$BASE/demo/runs/3/log?step=1" | tee "$RUN/nul-output.log"
grep -qF $'before\xEF\xBF\xBDafter\xEF\xBF\xBDstill-here' "$RUN/nul-output.log" || fail "NUL bytes were not replaced with U+FFFD in the stored log"
removed=false
for i in $(seq 1 10); do
  [ -z "$(steps)" ] && { removed=true; break; }
  sleep 1
done
$removed || fail "a step container outlived the malicious-pipeline run"

step "a flag-shaped image is rejected before it ever reaches docker"
git -C "$W" checkout -qb flag-image main
cat > "$W/.gitman.yml" <<'EOF'
image: --privileged
steps:
  - name: build
    run: echo unreachable
EOF
git -C "$W" add -A && git -C "$W" commit -qm "Flag-shaped image"
git -C "$W" push "$REMOTE" flag-image 2>&1 | tee "$RUN/push-flag-image.txt"
grep -q "failed before starting" "$RUN/push-flag-image.txt" || fail "a flag-shaped image was not rejected before starting"
grep -qi "not an image reference" "$RUN/push-flag-image.txt" || fail "the flag-shaped image's rejection reason is missing"

step "kill the worker mid-step: the run is failed and its container removed"
git -C "$W" checkout -qb slow/two main
git -C "$W" commit -q --allow-empty -m "Slow, again"
git -C "$W" push "$REMOTE" slow/two 2>&1 | tee "$RUN/push-slow2.txt"
RUN_NUM=$(grep -oE 'run #[0-9]+' "$RUN/push-slow2.txt" | grep -oE '[0-9]+')
for i in $(seq 1 60); do
  curl -s -b "$JAR" "$BASE/demo/runs/$RUN_NUM/log?step=1" | grep -q 'tick 3' && break
  sleep 1
done
curl -s -b "$JAR" "$BASE/demo/runs/$RUN_NUM/log?step=1" | grep -q 'tick 3' || fail "run #$RUN_NUM's slow step never started"
[ -n "$(steps)" ] || fail "no step container found running before the worker is killed"
kill -9 "$(cat "$RUN/worker.pid")"
wait "$(cat "$RUN/worker.pid")" 2>/dev/null || true
sleep 65
"$BIN" admin worker cleanup | tee "$RUN/worker-cleanup.txt"
grep -qE 'Failed [1-9][0-9]* runs' "$RUN/worker-cleanup.txt" || fail "admin worker cleanup did not fail the killed worker's run"
page=$(curl -s -b "$JAR" "$BASE/demo/runs/$RUN_NUM")
echo "$page" | grep -q 'status-lg is-failed' || fail "run #$RUN_NUM was not failed after its worker was killed"
echo "$page" | grep -q 'stopped responding' || fail "run #$RUN_NUM's failure reason does not explain that its worker was lost"
[ -z "$(steps)" ] || fail "the killed worker's step container was not removed by cleanup"

step "logs"
tail -5 "$RUN/web.log"
tail -8 "$RUN/worker.log"
echo
echo "END-TO-END OK"
