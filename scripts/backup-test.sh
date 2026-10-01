#!/usr/bin/env bash
# Restore source, grants, credential scope, secrets and retained run history.
source "$(dirname "${BASH_SOURCE[0]}")/integration-lib.sh"
integration_init backup "${GITMAN_BACKUP_PORT:-18082}"
bootstrap_admin
"$BIN" admin repo create demo
"$BIN" admin repo create private
"$BIN" admin person add mina >/dev/null
TOKEN=$("$BIN" admin token create --write --repos demo darius backup-check | awk '/^Token:/ {print $2}')
"$BIN" admin rule set --run demo branch main
git -C "$W" init -q -b main
git -C "$W" config user.name Integration
git -C "$W" config user.email integration@example.invalid
printf 'image: alpine:3.20\nsteps:\n  - name: test\n    run: echo hello\n' >"$W/.gitman.yml"
printf '# Backup check\n' >"$W/README.md"
git -C "$W" add -A
git -C "$W" commit -qm 'Retained source'
git -C "$W" push "http://darius:$TOKEN@127.0.0.1:$PORT/demo.git" main
HEAD=$(git -C "$W" rev-parse HEAD)
code=$(curl -sS -b "$JAR" -H 'Sec-Fetch-Site: same-origin' --data-urlencode key=DEPLOY_TOKEN --data-urlencode value=restore-secret -o /dev/null -w '%{http_code}' "$BASE/demo/settings/secrets")
[[ "$code" == 303 ]] || fail "secret creation: $code"
"$BIN" admin maintenance enable
"$BIN" admin maintenance status
"$BIN" admin maintenance backup "$RUN/snapshot"
kill "$(cat "$RUN/web.pid")"
wait "$(cat "$RUN/web.pid")" || true
# Drop only the unique database created by this script, then recreate it empty.
dropdb "$DATABASE"
createdb "$DATABASE"
export GITMAN_DATA_DIR="$RUN/restored"
"$BIN" restore "$RUN/snapshot"
"$BIN" admin maintenance status
"$BIN" web >>"$RUN/web.log" 2>&1 & echo $! >"$RUN/web.pid"
wait_for 30 curl -sf --max-time 1 "$BASE/readyz" -o /dev/null
"$BIN" admin maintenance disable
git clone -q "http://darius:$TOKEN@127.0.0.1:$PORT/demo.git" "$RUN/clone"
[[ $(git -C "$RUN/clone" rev-parse HEAD) == "$HEAD" ]] || fail 'restored clone changed commit'
[[ $(psql -d "$DATABASE" -tAc 'SELECT count(*) FROM runs') == 1 ]] || fail 'run history lost'
[[ $(psql -d "$DATABASE" -tAc 'SELECT count(*) FROM secrets') == 1 ]] || fail 'encrypted secret lost'
if git ls-remote "http://darius:$TOKEN@127.0.0.1:$PORT/private.git" >"$RUN/denied.log" 2>&1; then fail 'token repository scope lost'; fi
step 'BACKUP RESTORE OK'
