#!/usr/bin/env bash
# Pure offline regression tests for version/changelog/notes admission.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
run=$(mktemp -d "${TMPDIR:-/tmp}/gitman-release-metadata.XXXXXXXX")
trap 'rm -rf -- "$run"' EXIT
mkdir -p "$run/internal/web"
cat > "$run/CHANGELOG.md" <<'NOTES'
# Changelog

## Unreleased

## [1.0.0] - 2026-11-17

- A complete, verified clean-install release.
NOTES
cat > "$run/internal/web/releasenotes.json" <<'JSON'
{"releases":[{"version":"1.0.0","releasedOn":"2026-11-17","items":["Clean installation"]}]}
JSON
check() { node "$root/scripts/check-release-metadata.mjs" "$1" "$run"; }
reject() {
  if check "$1" > "$run/reject.log" 2>&1; then
    echo "metadata admission accepted $2" >&2
    exit 1
  fi
}
check v1.0.0
reject v1.0.1 'wrong release tag'
reject v01.0.0 'noncanonical version'
sed -i 's/2026-11-17/2026-11-18/g' "$run/internal/web/releasenotes.json"
reject v1.0.0 'mismatched release date'
sed -i 's/2026-11-18/2026-11-17/g' "$run/internal/web/releasenotes.json"
sed -i 's/## \[1.0.0\] - 2026-11-17/## [1.0.0] - 2026-13-17/' "$run/CHANGELOG.md"
reject v1.0.0 'invalid release date'
echo 'Release metadata admission regressions passed'
