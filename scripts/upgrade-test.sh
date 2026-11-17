#!/usr/bin/env bash
# Upgrade an installation made by an earlier release in place: GITMAN_OLD_BIN is
# that release's binary (for example built from its tag). History, tokens and
# secrets made by the old binary must survive, and the new binary must run a
# fresh pipeline against the same database and data directory.
# shellcheck source=scripts/integration-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/integration-lib.sh"
: "${GITMAN_OLD_BIN:?Set GITMAN_OLD_BIN to the previous release gitman binary}"
NEW_BIN=$(mktemp "${TMPDIR:-/tmp}/gitman-new.XXXXXXXX")
(cd "$ROOT" && go build -o "$NEW_BIN" ./cmd/gitman)
GITMAN_INTEGRATION_BIN=$GITMAN_OLD_BIN integration_init upgrade "${GITMAN_UPGRADE_PORT:-18083}"
trap 'rm -f "$NEW_BIN"; integration_cleanup' EXIT
OLD_VERSION=$("$BIN" version)
start_worker
bootstrap_admin
"$BIN" admin repo create demo
TOKEN=$("$BIN" admin token create --write --repos demo darius upgrade | awk '/^Token:/ {print $2}')
"$BIN" admin rule set --run demo branch main
git -C "$W" init -q -b main
git -C "$W" config user.name Integration && git -C "$W" config user.email integration@example.invalid
printf 'image: alpine:3.20\nsteps:\n  - name: build\n    run: echo before-upgrade\n' >"$W/.gitman.yml"
git -C "$W" add -A && git -C "$W" commit -qm 'before'
git -C "$W" push -q "http://darius:$TOKEN@127.0.0.1:$PORT/demo.git" main 2>&1 | tail -2
settled demo 1
[[ $(psql -d "$DATABASE" -tAc "SELECT status FROM runs WHERE number=1") == passed ]] || fail "run 1 did not pass before the upgrade"

step "stop the old release, start the new one on the same data"
for f in worker.pid web.pid; do kill "$(cat "$RUN/$f")"; wait "$(cat "$RUN/$f")" 2>/dev/null || true; done
BIN=$NEW_BIN
"$BIN" admin migrate
"$BIN" version
echo "upgraded from $OLD_VERSION to $("$BIN" version)"
"$BIN" web >>"$RUN/web.log" 2>&1 & echo $! >"$RUN/web.pid"
wait_for 30 curl -sf --max-time 1 "$BASE/readyz" -o /dev/null
start_worker

step "history, logs and tokens survived"
[[ $(psql -d "$DATABASE" -tAc "SELECT status FROM runs WHERE number=1") == passed ]] || fail "old run lost"
curl -sf -b "$JAR" "$BASE/demo/runs/1" | grep -q before-upgrade || fail "old run output lost"
step "a new push runs on the upgraded installation"
printf 'image: alpine:3.20\nsteps:\n  - name: build\n    run: echo after-upgrade\n' >"$W/.gitman.yml"
git -C "$W" commit -qam 'after'
git -C "$W" push -q "http://darius:$TOKEN@127.0.0.1:$PORT/demo.git" main 2>&1 | tail -2
settled demo 2
[[ $(psql -d "$DATABASE" -tAc "SELECT status FROM runs WHERE number=2") == passed ]] || fail "run 2 did not pass after the upgrade"
"$BIN" doctor || true
echo "UPGRADE OK"
