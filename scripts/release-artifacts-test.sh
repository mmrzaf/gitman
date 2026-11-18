#!/usr/bin/env bash
# Regression tests for the release-bundle admission gate. No Go, Docker or DB.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
run=$(mktemp -d "${TMPDIR:-/tmp}/gitman-artifacts-test.XXXXXXXX")
trap 'rm -rf -- "$run"' EXIT
mkdir -p "$run/release" "$run/source/gitman-1.0.0/internal/postgres/migrations"
printf 'module example.invalid/gitman\n' >"$run/source/gitman-1.0.0/go.mod"
printf '%s\n' 'CREATE TABLE test (id integer);' >"$run/source/gitman-1.0.0/internal/postgres/migrations/0001_init.sql"
tar -C "$run/source" -czf "$run/release/gitman-1.0.0.tar.gz" gitman-1.0.0
printf 'test Docker archive\n' | gzip >"$run/release/gitman-docker-image-linux-amd64-1.0.0.tar.gz"
cat >"$run/release/gitman_linux_amd64" <<'SCRIPT'
#!/bin/sh
printf 'gitman v1.0.0\n'
SCRIPT
cat >"$run/release/gitman_linux_arm64" <<'SCRIPT'
#!/bin/sh
printf 'gitman v1.0.0\n'
SCRIPT
chmod +x "$run/release/gitman_linux_amd64" "$run/release/gitman_linux_arm64"
checksums() {
	(cd "$run/release" && sha256sum -- gitman_linux_amd64 gitman_linux_arm64 gitman-1.0.0.tar.gz gitman-docker-image-linux-amd64-1.0.0.tar.gz | sort -k2 >SHA256SUMS)
}
reject() {
	if "$root/scripts/verify-release-artifacts.sh" v1.0.0 "$run/release" >"$run/rejection.log" 2>&1; then
		echo "release artifact checker accepted $1" >&2
		exit 1
	fi
}
checksums
"$root/scripts/verify-release-artifacts.sh" v1.0.0 "$run/release"
# Artifact transports drop the execute bit; the verifier must still smoke-test it.
chmod -x "$run/release/gitman_linux_amd64" "$run/release/gitman_linux_arm64"
"$root/scripts/verify-release-artifacts.sh" v1.0.0 "$run/release"
printf 'tamper\n' >>"$run/release/gitman_linux_arm64"
reject 'tampered binary'
checksums
printf 'unexpected\n' >"$run/release/extra.txt"
reject 'extra unchecked artifact'
rm "$run/release/extra.txt"
# Even checksummed artifacts are rejected if the source contains a secret.
printf 'PRIVATE\n' >"$run/source/gitman-1.0.0/.env"
tar -C "$run/source" -czf "$run/release/gitman-1.0.0.tar.gz" gitman-1.0.0
checksums
reject 'checksummed source archive with .env'
echo 'Release artifact verifier self-tests passed'
