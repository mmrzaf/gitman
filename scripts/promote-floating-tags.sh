#!/usr/bin/env bash
# Update floating tags only for the currently published latest stable release.
# Arguments: <vX.Y.Z> <registry/image>; GH_TOKEN is supplied by the workflow.
set -euo pipefail
version=${1:?usage: promote-floating-tags.sh <vX.Y.Z> <registry/image>}
image=${2:?usage: promote-floating-tags.sh <vX.Y.Z> <registry/image>}
[[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
  echo "Floating aliases are only for stable semantic versions, got $version" >&2
  exit 1
}
[[ "$image" == */* && "$image" != *' '* ]] || {
  echo "Expected an explicit registry/image reference" >&2
  exit 1
}
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY must name the release repository}"
command -v gh >/dev/null || { echo 'GitHub CLI is required' >&2; exit 1; }
command -v docker >/dev/null || { echo 'Docker is required' >&2; exit 1; }
# GitHub's latest release excludes drafts and prereleases. Fail closed if
# GitHub is unavailable: never guess which published version is newest.
latest=$(gh api "repos/${GITHUB_REPOSITORY}/releases/latest" --jq '.tag_name')
[[ "$latest" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
  echo "GitHub did not return a valid latest stable release: $latest" >&2
  exit 1
}
if [[ "$latest" != "$version" ]]; then
  printf 'Skipping floating aliases: latest stable published release is %s, not %s\n' "$latest" "$version"
  exit 0
fi
clean=${version#v}
source="${image}:${clean}"
major_minor=${clean%.*}
docker buildx imagetools inspect "$source" >/dev/null
docker buildx imagetools create \
  --tag "${image}:${major_minor}" \
  --tag "${image}:latest" \
  "$source"
# Both aliases are important. Ensure the registry accepted them.
docker buildx imagetools inspect "${image}:${major_minor}" >/dev/null
docker buildx imagetools inspect "${image}:latest" >/dev/null
printf 'Promoted floating tags to %s\n' "$source"
