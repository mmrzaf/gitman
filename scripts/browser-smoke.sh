#!/usr/bin/env bash
# Rendered UI checks without a Docker daemon; full live execution is browser.sh.
source "$(dirname "${BASH_SOURCE[0]}")/integration-lib.sh"
integration_init smoke "${GITMAN_BROWSER_PORT:-18081}"
bootstrap_admin
"$BIN" admin repo create --description 'Payments, refunds and reconciliation across multiple providers and currencies' demo >/dev/null
"$BIN" admin repo create empty >/dev/null
"$BIN" admin person add mina >/dev/null
"$BIN" admin rule set --run demo branch main >/dev/null
TOKEN=$("$BIN" admin token create --write --repos demo darius laptop | awk '/^Token:/ {print $2}')
git -C "$W" init -q -b main
git -C "$W" config user.name Integration
git -C "$W" config user.email integration@example.invalid
printf 'image: alpine:3.20\nsteps:\n  - name: test\n    run: echo hello\n' >"$W/.gitman.yml"
mkdir -p "$W/docs"
printf '# Payments\n' >"$W/README.md"
printf '# Deployment\n\nReview the deployment configuration before starting the service.\n' >"$W/docs/deployment-and-disaster-recovery-procedures.md"
git -C "$W" add -A
git -C "$W" commit -qm 'Start payments'
git -C "$W" tag v1.0.0
printf '\nReconcile transactions across providers before issuing refunds.\n' >>"$W/README.md"
git -C "$W" commit -qam 'Document reconciliation across payment providers before issuing refunds and completing settlement'
git -C "$W" branch feature/reconciliation-across-payment-providers-and-currencies
git -C "$W" tag v1.1.0
git -C "$W" push -q "http://darius:$TOKEN@127.0.0.1:$PORT/demo.git" main feature/reconciliation-across-payment-providers-and-currencies --tags
"$BIN" admin token create --repos demo darius production-deployment-and-disaster-recovery >/dev/null
curl -sS -b "$JAR" -H 'Sec-Fetch-Site: same-origin' --data-urlencode key=DEPLOY_TOKEN --data-urlencode value=smoke-secret -o /dev/null "$BASE/demo/settings/secrets"
BASE="$BASE" PASSWORD="$PASSWORD" SHOTS="$RUN/screenshots" node "$ROOT/scripts/browser-smoke.mjs"
