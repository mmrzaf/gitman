#!/usr/bin/env bash
# Restore source, reader grants, token scope, secret values and retained history.
source "$(dirname "${BASH_SOURCE[0]}")/integration-lib.sh"
integration_init backup "${GITMAN_BACKUP_PORT:-18082}"
bootstrap_admin
"$BIN" admin repo create demo
"$BIN" admin repo create private
"$BIN" admin repo visibility demo restricted
"$BIN" admin repo visibility private restricted
"$BIN" admin person add mina >"$RUN/admin.txt"
bootstrap=$(awk '/^Password:/ {print $2}' "$RUN/admin.txt")
reader_password=$(openssl rand -base64 24)
reader_jar="$RUN/reader-cookies"
code=$(curl -sS -c "$reader_jar" -o /dev/null -w '%{http_code}' -H 'Sec-Fetch-Site: same-origin' --data-urlencode username=mina --data-urlencode "password=$bootstrap" "$BASE/login")
[[ "$code" == 303 ]] || fail "reader login: $code"
code=$(curl -sS -b "$reader_jar" -c "$reader_jar" -o /dev/null -w '%{http_code}' -H 'Sec-Fetch-Site: same-origin' --data-urlencode "current_password=$bootstrap" --data-urlencode "new_password=$reader_password" --data-urlencode "confirm_password=$reader_password" "$BASE/me/password")
[[ "$code" == 303 ]] || fail "reader password change: $code"
rm -f "$RUN/admin.txt" "$reader_jar"
"$BIN" admin reader add demo mina
READER_TOKEN=$("$BIN" admin token create mina restored-reader | awk '/^Token:/ {print $2}')
TOKEN=$("$BIN" admin token create --write --repos demo darius backup-check | awk '/^Token:/ {print $2}')
"$BIN" admin rule set --run --force --deploy demo branch main
git -C "$W" init -q -b main
git -C "$W" config user.name Integration
git -C "$W" config user.email integration@example.invalid
cat >"$W/.gitman.yml" <<'PIPELINE'
image: alpine:3.20
targets:
  staging:
    branch: main
steps:
  - name: deploy
    type: deploy
    when: target
    run: echo deployed
PIPELINE
printf '# Backup check\n' >"$W/README.md"
git -C "$W" add -A
git -C "$W" commit -qm 'Retained source'
git -C "$W" push "http://darius:$TOKEN@127.0.0.1:$PORT/demo.git" main
retained_commit=$(git -C "$W" rev-parse HEAD)
# Seed completed history independently of Docker: this check tests restoration,
# while e2e.sh tests execution and receipt recording against a real daemon.
psql -X -v ON_ERROR_STOP=1 -d "$DATABASE" <<'SQL'
BEGIN;
UPDATE runs SET status='passed',started_at=now()-interval '1 minute',finished_at=now() WHERE number=1;
UPDATE steps SET status='passed',exit_code=0,started_at=now()-interval '1 minute',finished_at=now();
INSERT INTO step_logs(repo_id,step_id,sequence,content,byte_len,line_count)
 SELECT r.repo_id,s.id,1,'backup output',octet_length('backup output'),1 FROM steps s JOIN runs r ON r.id=s.run_id WHERE r.number=1;
UPDATE steps SET log_bytes=octet_length('backup output'),log_lines=1;
INSERT INTO deployments(id,repo_id,target,version,commit_hash,run_id,step_id,person_id)
 SELECT 'backup-deployment',r.repo_id,r.target,r.version,r.commit_hash,r.id,s.id,r.triggered_by FROM runs r JOIN steps s ON s.run_id=r.id WHERE r.number=1;
INSERT INTO run_summary(run_id,key,value) SELECT id,'fixture','retained summary' FROM runs WHERE number=1;
COMMIT;
SQL
fixture_secret=$(openssl rand -hex 24)
secret_fingerprint=$(printf '%s' "$fixture_secret" | sha256sum | awk '{print $1}')
code=$(curl -sS -b "$JAR" -H 'Sec-Fetch-Site: same-origin' --data-urlencode key=DEPLOY_TOKEN --data-urlencode "value=$fixture_secret" -o /dev/null -w '%{http_code}' "$BASE/demo/settings/secrets")
[[ "$code" == 303 ]] || fail "secret creation: $code"
(cd "$ROOT" && go run ./scripts/backup-check --repository demo --secret DEPLOY_TOKEN --sha256 "$secret_fingerprint")
# Replace the public history so restoring the old run must preserve a pinned
# commit, rather than accidentally relying on the default branch to retain it.
"$BIN" admin rule set --force demo branch main
git -C "$W" checkout -q --orphan replacement
git -C "$W" rm -q -rf .
printf '# Replacement source\n' >"$W/README.md"
git -C "$W" add README.md
git -C "$W" commit -qm 'Replacement history'
git -C "$W" push --force "http://darius:$TOKEN@127.0.0.1:$PORT/demo.git" HEAD:main
current_commit=$(git -C "$W" rev-parse HEAD)
repo_id=$(psql -X -d "$DATABASE" -tAc "SELECT id FROM repos WHERE name='demo'")
repo_path="$GITMAN_DATA_DIR/repos/$repo_id.git"
git --git-dir="$repo_path" reflog expire --expire=now --all
git --git-dir="$repo_path" gc --prune=now
"$BIN" admin maintenance enable
"$BIN" admin maintenance backup "$RUN/snapshot"
kill "$(cat "$RUN/web.pid")"
wait "$(cat "$RUN/web.pid")" || true
# Drop only the unique database created by this script, then recreate it empty.
dropdb "$DATABASE"
createdb "$DATABASE"
export GITMAN_DATA_DIR="$RUN/restored"
"$BIN" restore "$RUN/snapshot"
"$BIN" web >>"$RUN/web.log" 2>&1 & echo $! >"$RUN/web.pid"
wait_for 30 curl -sf --max-time 1 "$BASE/readyz" -o /dev/null
"$BIN" admin maintenance disable
(cd "$ROOT" && go run ./scripts/backup-check --repository demo --secret DEPLOY_TOKEN --sha256 "$secret_fingerprint")
git clone -q "http://mina:$READER_TOKEN@127.0.0.1:$PORT/demo.git" "$RUN/clone"
[[ $(git -C "$RUN/clone" rev-parse HEAD) == "$current_commit" ]] || fail 'restored clone changed commit'
if git -C "$RUN/clone" merge-base --is-ancestor "$retained_commit" HEAD 2>/dev/null; then fail 'retained commit still belongs to public history'; fi
git -C "$RUN/clone" fetch -q origin "$retained_commit"
[[ $(git -C "$RUN/clone" show "$retained_commit:README.md") == '# Backup check' ]] || fail 'retained source lost'
[[ $(psql -X -d "$DATABASE" -tAc "SELECT count(*) FROM runs WHERE number=1 AND status='passed' AND commit_hash='$retained_commit' AND finished_at IS NOT NULL") == 1 ]] || fail 'completed run history lost'
[[ $(psql -X -d "$DATABASE" -tAc "SELECT content FROM step_logs") == 'backup output' ]] || fail 'step output lost'
[[ $(psql -X -d "$DATABASE" -tAc "SELECT value FROM run_summary WHERE key='fixture'") == 'retained summary' ]] || fail 'run summary lost'
[[ $(psql -X -d "$DATABASE" -tAc "SELECT count(*) FROM deployments WHERE target='staging' AND commit_hash='$retained_commit' AND run_id IS NOT NULL AND step_id IS NOT NULL") == 1 ]] || fail 'deployment history lost'
if git ls-remote "http://mina:$READER_TOKEN@127.0.0.1:$PORT/private.git" >"$RUN/reader-denied.log" 2>&1; then fail 'reader gained ungranted access'; fi
if git ls-remote "http://darius:$TOKEN@127.0.0.1:$PORT/private.git" >"$RUN/denied.log" 2>&1; then fail 'token repository scope lost'; fi
"$BIN" admin reader remove demo mina
if git ls-remote "http://mina:$READER_TOKEN@127.0.0.1:$PORT/demo.git" >"$RUN/revoked-reader.log" 2>&1; then fail 'revoked reader retained access'; fi
step 'BACKUP RESTORE OK'
