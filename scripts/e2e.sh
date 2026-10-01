#!/usr/bin/env bash
# Real Git HTTP, Docker execution, cancellation, lost-worker cleanup and output safety.
source "$(dirname "${BASH_SOURCE[0]}")/integration-lib.sh"
integration_init e2e "${GITMAN_E2E_PORT:-18080}"
start_worker
INSTANCE=$(psql -d "$DATABASE" -tAc 'SELECT id FROM instance')
# steps lists this instance's running step containers.
steps() { docker ps -q --filter "label=gitman.instance=$INSTANCE" "$@"; }

step "admin, repository, write token"
bootstrap_admin
"$BIN" admin repo create --description "End-to-end" demo
TOKEN=$("$BIN" admin token create --write --repos demo darius laptop | awk '/^Token:/ {print $2}')
[ -n "$TOKEN" ] || fail "no token"
"$BIN" admin rule set --run --deploy demo branch main
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
  - name: deploy
    type: deploy
    when: staging
    run: echo "shipping $GITMAN_VERSION to $GITMAN_TARGET"
EOF
printf '<html><script>alert(document.cookie)</script></html>\n' > "$W/index.html"
git -C "$W" add -A && git -C "$W" commit -qm "Pipeline and a page"
REMOTE="http://darius:$TOKEN@127.0.0.1:$PORT/demo.git"
git -C "$W" push "$REMOTE" main 2>&1 | tee "$RUN/push-main.txt"
grep -q "run #1 queued for branch main, target context staging" "$RUN/push-main.txt" || fail "push did not report run #1"

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
echo "$repo_page" | grep -q '<th scope="row" class="table-rowheader">staging</th>' || fail "deployment not shown on the repository page"
curl -s -b "$JAR" "$BASE/" | grep -q '>staging</td>' || fail "deployment not shown on Home"
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
psql -d "$DATABASE" -v ON_ERROR_STOP=1 -c "UPDATE workers SET heartbeat_at=now()-interval '6 minutes' WHERE id=(SELECT worker_id FROM runs WHERE number=$RUN_NUM AND repo_id=(SELECT id FROM repos WHERE name='demo'))"
"$BIN" admin worker cleanup | tee "$RUN/worker-cleanup.txt"
# The web reaper may settle the run before the cleanup command; assert the final state below.
page=$(curl -s -b "$JAR" "$BASE/demo/runs/$RUN_NUM")
echo "$page" | grep -q 'status-lg is-failed' || fail "run #$RUN_NUM was not failed after its worker was killed"
echo "$page" | grep -q 'stopped responding' || fail "run #$RUN_NUM's failure reason does not explain that its worker was lost"
[ -z "$(steps)" ] || fail "the killed worker's step container was not removed by cleanup"

step "logs"
tail -5 "$RUN/web.log"
tail -8 "$RUN/worker.log"
echo
echo "END-TO-END OK"
