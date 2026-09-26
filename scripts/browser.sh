#!/usr/bin/env bash
# Starts a fresh Gitman (web + worker) against a real database, seeds it
# with what a small team's instance holds — people, repositories, rules,
# secrets, tokens, and runs that passed, failed and shipped to two
# targets — then hands off to browser.mjs to drive it with a real,
# headless Chromium: every screen, every interactive component by
# keyboard, live updates and log streaming, an accessibility scan of
# every screen in both themes, every page with JavaScript disabled, and
# a screenshot of every screen in both themes.
#
# Requires everything e2e.sh does — including its PGHOST/PGPORT/PGUSER —
# plus Node with Playwright and a Chromium for it:
#   cd scripts && npm install && npx playwright install chromium
# or, to use a Chromium already on the machine, set
# PLAYWRIGHT_EXECUTABLE_PATH to it. It serves on GITMAN_BROWSER_PORT
# (18081 by default) and writes its screenshots to .data/browser/screenshots.
# Run from anywhere; it always operates on the repository this script
# lives in:
#   scripts/browser.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

export PGHOST=${PGHOST:-127.0.0.1} PGPORT=${PGPORT:-5432} PGUSER=${PGUSER:-postgres}
BIN="$ROOT/bin/gitman"
RUN="$ROOT/.data/browser"
DATA="$RUN/data"
W="$RUN/work"
PORT=${GITMAN_BROWSER_PORT:-18081}
BASE=http://127.0.0.1:$PORT
export GITMAN_DATABASE_URL="postgres://$PGUSER@$PGHOST:$PGPORT/gitman_browser?sslmode=disable"
export GITMAN_DATA_DIR=$DATA
export GITMAN_PUBLIC_URL=$BASE
export GITMAN_PORT=$PORT
export GITMAN_SECRET_KEY='a browser-check secret key, at least 32 bytes'

# cleanup stops what the script started. A process may be gone already —
# the worker is killed on purpose — so a failed kill is not a failure.
cleanup() {
  for pid in "$RUN/web.pid" "$RUN/worker.pid"; do
    [ -f "$pid" ] && kill "$(cat "$pid")" 2>/dev/null || true
  done
}
trap cleanup EXIT

echo "=== build"
go build -o "$BIN" ./cmd/gitman

echo "=== fresh database and data directory"
psql -qc 'DROP DATABASE IF EXISTS gitman_browser' -c 'CREATE DATABASE gitman_browser'
rm -rf "$RUN" && mkdir -p "$DATA" "$W"
if curl -s -o /dev/null "$BASE/"; then echo "something is already serving $BASE; set GITMAN_BROWSER_PORT"; exit 1; fi

echo "=== start web and worker"
"$BIN" web > "$RUN/web.log" 2>&1 &
echo $! > "$RUN/web.pid"
for i in $(seq 1 50); do curl -sf "$BASE/readyz" >/dev/null && break; sleep 0.2; done
curl -sf "$BASE/readyz" >/dev/null || { echo "web did not become ready"; exit 1; }
"$BIN" worker > "$RUN/worker.log" 2>&1 &
echo $! > "$RUN/worker.pid"
sleep 1

echo "=== people, repositories, rules, tokens"
"$BIN" admin person add --admin darius > "$RUN/admin.txt"
PASSWORD=$(awk '/^Password:/ {print $2}' "$RUN/admin.txt")
"$BIN" admin person add mina >/dev/null
"$BIN" admin person add --admin reza >/dev/null
"$BIN" admin person add arash >/dev/null
"$BIN" admin person disable arash >/dev/null
"$BIN" admin repo create --description "Payments API — the service behind checkout, refunds and payouts" demo >/dev/null
"$BIN" admin repo create --description "Marketing site, dashboard and admin" waiotech >/dev/null
"$BIN" admin repo create sms-gateway >/dev/null
"$BIN" admin repo create --description "Shared Terraform modules for every environment" infrastructure-terraform-modules-shared >/dev/null
TOKEN=$("$BIN" admin token create --write darius laptop | awk '/^Token:/ {print $2}')
"$BIN" admin token create --days 90 darius ci-readonly >/dev/null
"$BIN" admin rule set --run --ship demo branch main >/dev/null
"$BIN" admin rule set --run --ship waiotech branch main >/dev/null
"$BIN" admin rule set --run --ship --push admins demo tag 'v*' >/dev/null
"$BIN" admin rule set --run --force --delete demo branch 'feature/*' >/dev/null
"$BIN" admin rule set --run demo branch broken >/dev/null
"$BIN" admin rule set --run --secrets demo branch 'slow/*' >/dev/null

echo "=== push, run and ship"
git -C "$W" init -q -b main
git -C "$W" config user.name "Darius Rahimi" && git -C "$W" config user.email darius@example.com
cat > "$W/.gitman.yml" <<'EOF'
image: alpine:3.20
targets:
  staging:
    branch: main
  production:
    tag: "v*"
steps:
  - name: build
    run: |
      echo "building $GITMAN_REPO #$GITMAN_RUN"
      for i in $(seq 1 60); do echo "compile  example.com/payments/internal/pkg$i  ok"; done
      if [ "${GITMAN_REF#slow/}" != "$GITMAN_REF" ]; then sleep 4; fi
      echo "version=$GITMAN_VERSION" >> "$GITMAN_SUMMARY"
      echo "image=registry.local/$GITMAN_REPO:$GITMAN_SHORT" >> "$GITMAN_SUMMARY"
  - name: test
    run: |
      for i in $(seq 1 40); do echo "ok   example.com/payments/internal/pkg$i  0.0${i}s"; done
      if [ "$GITMAN_REF" = "broken" ]; then
        echo "--- FAIL: TestChargeIsIdempotent (0.01s)"
        echo "    charge_test.go:48: a second charge with key k1 made a new payment"
        echo "FAIL  example.com/payments/internal/pay  0.214s"
        exit 1
      fi
  - name: slow
    when: branch
    run: |
      if [ "${GITMAN_REF#slow/}" != "$GITMAN_REF" ]; then
        for i in $(seq 1 300); do echo "tick $i: replaying recorded request $i against the sandbox"; sleep 1; done
      fi
      echo slow step done
  - name: ship
    when: target
    run: echo "shipping $GITMAN_VERSION to $GITMAN_TARGET"
EOF
mkdir -p "$W/cmd/api" "$W/internal/pay" "$W/docs"
printf '# Payments API\n\nCheckout, refunds and payouts.\n' > "$W/README.md"
printf '<html><body>hello</body></html>\n' > "$W/index.html"
printf 'package main\n\nfunc main() {}\n' > "$W/cmd/api/main.go"
{
  printf 'package pay\n\n'
  for i in $(seq 1 30); do printf '// step%d is one stage of settling a charge.\nfunc step%d(amount int) int {\n\treturn amount + %d\n}\n\n' "$i" "$i" "$i"; done
} > "$W/internal/pay/charge.go"
printf '# Architecture\n' > "$W/docs/architecture.md"
git -C "$W" add -A && git -C "$W" commit -qm "Start the payments service"
REMOTE="http://darius:$TOKEN@127.0.0.1:$PORT/demo.git"
push() { git -C "$W" push -q "$@" >/dev/null 2>&1; }
# settled waits for a repository's run to end.
settled() {
  for i in $(seq 1 120); do
    case "$(psql -d gitman_browser -tAc "SELECT r.status FROM runs r JOIN repos p ON p.id = r.repo_id WHERE p.name = '$1' AND r.number = $2")" in
      passed|failed|cancelled) return 0 ;;
    esac
    sleep 0.5
  done
  echo "run $1 #$2 did not end"; exit 1
}
push "$REMOTE" main; settled demo 1
git -C "$W" tag v1.4.0 && push "$REMOTE" v1.4.0; settled demo 2
git -C "$W" checkout -qb broken && git -C "$W" commit -q --allow-empty -m "Retry declined charges once" && push "$REMOTE" broken; settled demo 3
git -C "$W" checkout -q main
sed -i 's/return amount + 3$/return amount + 3 - fee(amount)/' "$W/internal/pay/charge.go"
printf '// fee is what the card network keeps from a charge.\nfunc fee(amount int) int {\n\treturn amount / 50\n}\n' >> "$W/internal/pay/charge.go"
git -C "$W" commit -qam "Take the network fee when settling a charge" && push "$REMOTE" main; settled demo 4
COMMIT=$(git -C "$W" rev-parse HEAD)
FEATURE=feature/refund-idempotency-keys-for-partial-refunds-across-currencies
git -C "$W" checkout -qb "$FEATURE"
printf 'package pay\n\n// Refund returns amount to the card, once per key.\nfunc Refund(key string, amount int) error {\n\treturn nil\n}\n' > "$W/internal/pay/refund.go"
git -C "$W" add -A && git -C "$W" commit -qm "Make refunds idempotent per key" && push "$REMOTE" "$FEATURE"; settled demo 5
git -C "$W" checkout -q main
W2="$RUN/work2"; mkdir -p "$W2"; git -C "$W2" init -q -b main
git -C "$W2" config user.name "Mina Karimi" && git -C "$W2" config user.email mina@example.com
cp "$W/.gitman.yml" "$W2/" && printf '# Waiotech\n' > "$W2/README.md"
git -C "$W2" add -A && git -C "$W2" commit -qm "Import the site"
git -C "$W2" push -q "http://darius:$TOKEN@127.0.0.1:$PORT/waiotech.git" main >/dev/null 2>&1; settled waiotech 1

echo "=== secrets"
curl -s -c "$RUN/cookies" -H 'Sec-Fetch-Site: same-origin' \
  --data-urlencode username=darius --data-urlencode "password=$PASSWORD" -o /dev/null "$BASE/login"
for key in DEPLOY_TOKEN SENTRY_DSN; do
  code=$(curl -s -b "$RUN/cookies" -H 'Sec-Fetch-Site: same-origin' --data-urlencode "key=$key" --data-urlencode "value=v-$key" \
    -o /dev/null -w '%{http_code}' "$BASE/demo/settings/secrets")
  [ "$code" = 303 ] || { echo "saving $key answered $code"; exit 1; }
done
echo "seeded"

# The commands browser.mjs runs at set moments, quoted for the shell it
# runs them in. The worker is stopped by the PID it has when the command
# runs, and waited for until it is gone: node's shell is not its parent,
# so "wait" could not.
q() { printf '%q' "$1"; }
slow() { printf 'git -C %s checkout -qb %s main >/dev/null 2>&1; git -C %s commit -q --allow-empty -m %s >/dev/null; git -C %s push -q %s %s' \
  "$(q "$W")" "$1" "$(q "$W")" "$(q "$2")" "$(q "$W")" "$(q "$REMOTE")" "$1"; }
echo "=== handing off to the browser check"
BASE="$BASE" PASSWORD="$PASSWORD" COMMIT="$COMMIT" FEATURE="$FEATURE" SHOTS="$RUN/screenshots" \
  PUSH_SLOW="$(slow slow/one Slow)" PUSH_SLOW_AGAIN="$(slow slow/two 'Slow, again')" \
  STOP_WORKER="pid=\$(cat $(q "$RUN/worker.pid")); kill \$pid 2>/dev/null; while kill -0 \$pid 2>/dev/null; do sleep 0.1; done" \
  START_WORKER="$(q "$BIN") worker >> $(q "$RUN/worker2.log") 2>&1 & echo \$! > $(q "$RUN/worker.pid")" \
  node "$ROOT/scripts/browser.mjs"
