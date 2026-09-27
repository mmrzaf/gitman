#!/usr/bin/env bash
# Builds the source archive of a release from the commit checked out, and
# its checksum next to it:
#   scripts/release-source-archive.sh <version> [output-dir]
# It archives the commit, not the working tree, so local edits and
# deletions never reach a release.
set -euo pipefail

version="${1:?usage: scripts/release-source-archive.sh <version> [output-dir]}"
out_dir="${2:-dist}"
prefix="gitman-${version#v}"
archive="${prefix}.tar.gz"

files="$(git ls-tree -r --name-only HEAD)"

while IFS= read -r path; do
  case "$path" in
    .data/*|data/*|bin/*|dist/*|coverage.out|coverage.html|.env|*/.env|*.sqlite|*.sqlite-*|*.db|*.log)
      echo "refusing runtime file in source archive: $path" >&2
      exit 1
      ;;
  esac
done <<<"$files"

for required_dir in internal/postgres/migrations internal/web/templates internal/web/static; do
  if ! grep -q "^${required_dir}/" <<<"$files"; then
    echo "source archive is missing required embedded inputs: ${required_dir}/" >&2
    exit 1
  fi
done

mkdir -p "$out_dir"
git archive --format=tar.gz --prefix="${prefix}/" -o "$out_dir/$archive" HEAD
(cd "$out_dir" && sha256sum -- "$archive" >"$archive.sha256")
echo "$out_dir/$archive"
