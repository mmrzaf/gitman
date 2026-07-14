#!/usr/bin/env bash
set -euo pipefail

version="${1:?usage: scripts/release-source-archive.sh <version> [output-dir]}"
out_dir="${2:-dist}"
prefix="gitman-${version#v}"
archive="${out_dir}/${prefix}.tar.gz"
checksum="${archive}.sha256"

mkdir -p "$out_dir"

tmp_list="$(mktemp)"
trap 'rm -f "$tmp_list"' EXIT

git ls-files >"$tmp_list"

while IFS= read -r path; do
  case "$path" in
    ""|/*|../*|*/../*|.data/*|data/*|bin/*|dist/*|coverage.out|coverage.html|.env|*/.env|authorized_keys|*/authorized_keys|*.sqlite|*.sqlite-*|*.db|*.log)
      echo "refusing unsafe or runtime path in source archive: $path" >&2
      exit 1
      ;;
  esac
done <"$tmp_list"

for required_dir in migrations templates static; do
  if ! grep -q "^${required_dir}/" "$tmp_list"; then
    echo "source archive is missing required embedded inputs: ${required_dir}/" >&2
    exit 1
  fi
done

tar --format=ustar --owner=0 --group=0 --numeric-owner \
  --transform "s#^#${prefix}/#" \
  -czf "$archive" -T "$tmp_list"

tar -tzf "$archive" | while IFS= read -r path; do
  case "$path" in
    /*|*"/../"*|../*|"$prefix/.data/"*|"$prefix/data/"*|"$prefix/bin/"*|"$prefix/dist/"*)
      echo "archive contains unsafe or runtime path: $path" >&2
      exit 1
      ;;
  esac
done

sha256sum "$archive" >"$checksum"
echo "$archive"
