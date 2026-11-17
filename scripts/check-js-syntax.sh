#!/usr/bin/env bash
# Syntax-check the checked-in browser JavaScript and Node test harnesses.
# No npm install is needed; only the Node runtime is used.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
command -v node >/dev/null || {
  echo 'Node.js is required to validate checked-in JavaScript' >&2
  exit 1
}
while IFS= read -r -d '' file; do
  node --check "$file"
done < <(find internal/web/static/js scripts -type f \
  \( -name '*.js' -o -name '*.mjs' \) \
  ! -path 'scripts/node_modules/*' -print0)
