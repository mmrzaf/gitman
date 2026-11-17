#!/usr/bin/env bash
# Two workers on one Docker host: every run is taken exactly once and both
# workers do work. A run needing an image no worker has stays queued, and the
# run page and doctor say why.
# shellcheck source=scripts/integration-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/integration-lib.sh"
integration_init multiworker "${GITMAN_MULTIWORKER_PORT:-18084}"
bootstrap_admin
"$BIN" admin repo create demo
TOKEN=$("$BIN" admin token create --write --repos demo darius multiworker | awk '/^Token:/ {print $2}')
"$BIN" admin rule set --run demo branch 'job/*'
"$BIN" admin rule set --run demo branch 'blocked'
REMOTE="http://darius:$TOKEN@127.0.0.1:$PORT/demo.git"
step "two workers"
start_worker
mv "$RUN/worker.pid" "$RUN/worker1.pid"
start_worker
ready_workers() { [[ $(psql -d "$DATABASE" -tAc "SELECT count(*) FROM workers WHERE ready AND stopped_at IS NULL") -ge 2 ]]; }
wait_for 30 ready_workers
git -C "$W" init -q -b main
git -C "$W" config user.name Integration && git -C "$W" config user.email integration@example.invalid
printf 'image: alpine:3.20\nsteps:\n  - name: work\n    run: sleep 3\n' >"$W/.gitman.yml"
git -C "$W" add -A && git -C "$W" commit -qm jobs
for i in 1 2 3 4 5 6; do git -C "$W" push -q "$REMOTE" "main:job/$i" 2>&1 | tail -1; done
for n in 1 2 3 4 5 6; do settled demo "$n"; done
passed=$(psql -d "$DATABASE" -tAc "SELECT count(*) FROM runs WHERE status='passed'")
[[ $passed == 6 ]] || fail "$passed of 6 runs passed"
used=$(psql -d "$DATABASE" -tAc "SELECT count(DISTINCT worker_id) FROM runs")
[[ $used == 2 ]] || fail "$used worker(s) ran the jobs, want 2"

step "a run no worker can start says why and stays queued"
printf 'image: alpine:3.20\nrequires: [gitman-missing:1]\nsteps:\n  - name: work\n    run: echo never\n' >"$W/.gitman.yml"
git -C "$W" commit -qam blocked
git -C "$W" push -q "$REMOTE" main:blocked 2>&1 | tail -1
sleep 3
[[ $(psql -d "$DATABASE" -tAc "SELECT status FROM runs WHERE number=7") == queued ]] || fail "blocked run left the queue"
curl -sf -b "$JAR" "$BASE/demo/runs/7" | grep -q "gitman-missing:1" || fail "run page does not name the missing image"
report=$("$BIN" doctor 2>&1 || true)
grep -q "cannot start" <<<"$report" || fail "doctor does not report the blocked run"
echo "MULTIWORKER OK"
