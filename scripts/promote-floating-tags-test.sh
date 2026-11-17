#!/usr/bin/env bash
# Offline CLI-stub regressions, no GitHub credentials or Docker daemon.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
run=$(mktemp -d "${TMPDIR:-/tmp}/gitman-floating-tag-test.XXXXXXXX")
trap 'rm -rf -- "$run"' EXIT
mkdir -p "$run/bin"
cat >"$run/bin/gh" <<'SCRIPT'
#!/usr/bin/env bash
printf 'gh %s\n' "$*" >> "$COMMAND_LOG"
if [[ ${FAIL_GH:-0} == 1 ]]; then exit 1; fi
printf '%s\n' "$FAKE_LATEST"
SCRIPT
cat >"$run/bin/docker" <<'SCRIPT'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >> "$COMMAND_LOG"
SCRIPT
chmod +x "$run/bin/gh" "$run/bin/docker"
export PATH="$run/bin:$PATH" GITHUB_REPOSITORY=mmrzaf/gitman COMMAND_LOG="$run/commands"
export FAKE_LATEST=v2.3.2
bash "$root/scripts/promote-floating-tags.sh" v2.3.1 ghcr.io/mmrzaf/gitman
if grep -q '^docker ' "$COMMAND_LOG"; then
  echo 'An older rerun was allowed to modify Docker aliases' >&2; exit 1
fi
: > "$COMMAND_LOG"
export FAKE_LATEST=v2.3.1
bash "$root/scripts/promote-floating-tags.sh" v2.3.1 ghcr.io/mmrzaf/gitman
if [[ $(grep -c '^docker ' "$COMMAND_LOG") != 4 ]]; then
  echo 'Alias promotion did not inspect source, create tags, and verify both tags' >&2; exit 1
fi
grep -Fq 'docker buildx imagetools create --tag ghcr.io/mmrzaf/gitman:2.3 --tag ghcr.io/mmrzaf/gitman:latest ghcr.io/mmrzaf/gitman:2.3.1' "$COMMAND_LOG"
: > "$COMMAND_LOG"
export FAIL_GH=1
if bash "$root/scripts/promote-floating-tags.sh" v2.3.1 ghcr.io/mmrzaf/gitman >"$run/failure" 2>&1; then
  echo 'Promotion proceeded when GitHub API failed' >&2; exit 1
fi
if grep -q '^docker ' "$COMMAND_LOG"; then
  echo 'An API outage resulted in Docker changes' >&2; exit 1
fi
unset FAIL_GH
export FAKE_LATEST='not-a-tag'
if bash "$root/scripts/promote-floating-tags.sh" v2.3.1 ghcr.io/mmrzaf/gitman >"$run/failure" 2>&1; then
  echo 'Malformed GitHub release metadata allowed promotion' >&2; exit 1
fi
if bash "$root/scripts/promote-floating-tags.sh" v2.3.1-rc.1 ghcr.io/mmrzaf/gitman >"$run/failure" 2>&1; then
  echo 'Prerelease was allowed to promote stable aliases' >&2; exit 1
fi
printf 'Floating Docker alias safety regressions passed\n'
