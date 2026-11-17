#!/usr/bin/env bash
# Fresh installation: empty database, first administrator, repository,
# authenticated Git push and fetch. Does not require a Docker worker.
# shellcheck source=scripts/integration-lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/integration-lib.sh"
integration_init clean "${GITMAN_CLEAN_INSTALL_PORT:-18083}"

step 'migrations applied to a fresh database'
expected=$(find "$ROOT/internal/postgres/migrations" -maxdepth 1 -type f -name '*.sql' | wc -l)
actual=$(psql -d "$DATABASE" -tAc 'SELECT count(*) FROM schema_migrations')
[[ $expected -gt 0 && $actual -eq $expected ]] || fail "migration ledger has $actual rows; expected $expected"
[[ $(psql -d "$DATABASE" -tAc 'SELECT count(*) FROM schema_migrations WHERE length(checksum) <> 64') == 0 ]] || fail 'invalid migration checksums'
[[ $(psql -d "$DATABASE" -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('image_hosts','repository_operations','sessions','events')") == 4 ]] || fail 'installed schema is incomplete'
ledger=$(psql -d "$DATABASE" -tAc "SELECT md5(string_agg(version::text || ':' || name || ':' || checksum, ',' ORDER BY version)) FROM schema_migrations")

step 'create first administrator, repository and token'
bootstrap_admin
"$BIN" admin repo create demo
TOKEN=$("$BIN" admin token create --write --repos demo darius clean-install | awk '/^Token:/ {print $2}')
[[ -n "$TOKEN" ]] || fail 'no write token created'

step 'push and read back a real commit'
git -C "$W" init -q -b main
git -C "$W" config user.name 'Gitman Install Test'
git -C "$W" config user.email 'install@example.invalid'
printf 'Gitman clean-install test\n' > "$W/README.md"
git -C "$W" add README.md
git -C "$W" commit -qm 'Initial commit'
REMOTE="http://darius:$TOKEN@127.0.0.1:$PORT/demo.git"
git -C "$W" push -q "$REMOTE" main
local_head=$(git -C "$W" rev-parse HEAD)
remote_head=$(git ls-remote "$REMOTE" refs/heads/main | cut -f1)
[[ "$remote_head" == "$local_head" ]] || fail 'pushed commit not readable from Gitman'
[[ $(psql -d "$DATABASE" -tAc "SELECT count(*) FROM refs WHERE name='main'") == 1 ]] || fail 'push did not record the branch'

step 'doctor and repeat migration'
"$BIN" admin migrate
"$BIN" doctor > "$RUN/doctor.log"
[[ $(psql -d "$DATABASE" -tAc "SELECT md5(string_agg(version::text || ':' || name || ':' || checksum, ',' ORDER BY version)) FROM schema_migrations") == "$ledger" ]] || fail 'migration reran or modified ledger' 
echo 'CLEAN INSTALL OK'
