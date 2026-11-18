#!/usr/bin/env bash
# Validate the exact distributable set before creating or publishing a release.
# Usage: scripts/verify-release-artifacts.sh <vX.Y.Z[-pre]> <artifact-dir>
set -euo pipefail

version=${1:?usage: verify-release-artifacts.sh <version> <artifact-dir>}
dir=${2:?usage: verify-release-artifacts.sh <version> <artifact-dir>}
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || {
  echo "invalid release version: $version" >&2
  exit 1
}
[[ -d "$dir" ]] || { echo "missing artifact directory: $dir" >&2; exit 1; }
clean=${version#v}
required=(
  gitman_linux_amd64
  gitman_linux_arm64
  "gitman-${clean}.tar.gz"
  "gitman-docker-image-linux-amd64-${clean}.tar.gz"
)
for name in "${required[@]}"; do
  [[ -s "$dir/$name" && -f "$dir/$name" && ! -L "$dir/$name" ]] || {
    echo "missing, empty or unsafe artifact: $name" >&2
    exit 1
  }
done
[[ -f "$dir/SHA256SUMS" && ! -L "$dir/SHA256SUMS" ]] || {
  echo 'missing SHA256SUMS' >&2
  exit 1
}
# Accept exactly the files this release produces: no unnoticed or unchecksummed
# attachments, stale binaries or accidental files from another version.
actual=$(find "$dir" -maxdepth 1 -type f -printf '%f\n' | LC_ALL=C sort)
expected=$(printf '%s\n' "${required[@]}" SHA256SUMS | LC_ALL=C sort)
[[ "$actual" == "$expected" ]] || {
  echo 'release artifact set does not match the expected four files + SHA256SUMS' >&2
  diff -u <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") >&2 || true
  exit 1
}
expected_checksums=$(printf '%s\n' "${required[@]}" | LC_ALL=C sort)
actual_checksums=$(awk '{print $2}' "$dir/SHA256SUMS" | sed 's/^\*//' | LC_ALL=C sort)
[[ "$expected_checksums" == "$actual_checksums" ]] || {
  echo 'SHA256SUMS does not cover exactly the release artifacts' >&2
  exit 1
}
(cd "$dir" && sha256sum -c --status SHA256SUMS) || {
  echo 'release checksum verification failed' >&2
  exit 1
}

# Check all archive member names before examining their contents. No runtime
# data or secrets belong in a distributable archive.
source_archive="$dir/gitman-${clean}.tar.gz"
source_list=$(tar -tzf "$source_archive") || { echo 'unreadable source archive' >&2; exit 1; }
grep -Fxq "gitman-${clean}/go.mod" <<<"$source_list" || {
  echo 'missing go.mod in source archive' >&2; exit 1;
}
grep -Eq "^gitman-${clean}/internal/postgres/migrations/[0-9]+_[^/]+\.sql$" <<<"$source_list" || {
  echo 'source archive missing SQL migrations' >&2; exit 1;
}
while IFS= read -r item; do
  case "$item" in
    "gitman-${clean}/"*) ;;
    *) echo "unsafe archive prefix: $item" >&2; exit 1 ;;
  esac
  case "$item" in
    *'/../'*|*'/./'*|*'/..'|*'/.git/'*|*'/.data/'*|*'/data/'*|*'/gitman-qa/'*|*'/bin/'*|*'/dist/'*|*'/.env'|*'/.env.'*|*'.pem'|*'.key'|*'.dump')
      [[ "$item" == "gitman-${clean}/.env.example" ]] && continue
      echo "unsafe file in source archive: $item" >&2
      exit 1
      ;;
  esac
done <<< "$source_list"
# Verify the offline image archive is intact; Docker-load smoke tests run in CI.
gzip -t "$dir/gitman-docker-image-linux-amd64-${clean}.tar.gz"
[[ $(uname -s) != Linux || $(uname -m) != x86_64 ]] || {
  [[ "$("$dir/gitman_linux_amd64" version)" == "gitman $version" ]] || {
    echo 'release binary reports the wrong version' >&2
    exit 1
  }
}
echo "Verified Gitman ${version}: four distributables, complete checksums, source safety and image archive"
