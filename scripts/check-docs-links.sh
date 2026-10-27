#!/usr/bin/env bash
# Checks every relative Markdown link in the docs, README, CHANGELOG and
# ROADMAP points at a file that exists. External links and in-page anchors
# are left alone: CI cannot rely on the network.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

status=0
while IFS= read -r file; do
  dir=$(dirname "$file")
  while IFS= read -r target; do
    case "$target" in
      ''|\#*|http://*|https://*|mailto:*|tel:*) continue ;;
    esac
    path=${target%%#*}
    path=${path%%\?*}
    [ -z "$path" ] && continue
    if [ ! -e "$dir/$path" ]; then
      printf '%s: broken link %s\n' "$file" "$target" >&2
      status=1
    fi
  done < <(grep -oE '\]\([^)]+\)' "$file" | sed -E 's/^\]\((.*)\)$/\1/')
done < <(find . -path './.git' -prune -o -path './scripts/node_modules' -prune -o \
  \( -name '*.md' -o -name 'README.md' \) -print | sort)

exit "$status"
