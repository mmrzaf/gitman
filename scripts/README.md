# Integration checks

These scripts build a temporary binary and create a unique disposable PostgreSQL
database and working directory per invocation. They never drop an existing
database or reuse an installation's data directory. PostgreSQL connection
settings use `PGHOST`, `PGPORT` and `PGUSER` (defaults: localhost, 5432, postgres).
Temporary working directories and test credentials are removed when tests finish.
Set `GITMAN_INTEGRATION_ARTIFACTS` to retain selected logs and screenshots for debugging. Treat those artifacts as sensitive test data.

- `e2e.sh`: real Git pushes, Docker pipelines, cancellation, malicious input,
  raw-file security headers and recovery after a killed worker. The lost-worker
  fixture is deliberately aged past the five-minute threshold to avoid a long
  sleep; assertions check the resulting run state and container cleanup.
- `multiworker.sh`: two workers on one Docker host take six runs exactly once
  each and both do work; a run needing an image no worker has stays queued
  while its page and `gitman doctor` name the image. Default port: 18084.
- `clean-install-test.sh`: initialize an empty PostgreSQL database with the
  embedded migrations, create an admin and repository, push/fetch a commit
  over authenticated Git HTTP, and verify idempotent migrations. No Docker
  worker or previous Gitman binary required. Default port: 18083.
- `browser.sh` and `browser.mjs`: real Docker execution with live page updates,
  keyboard interactions, axe accessibility checks, mobile layouts, both themes,
  JavaScript and plain HTML fallbacks. Default port: 18081.
- `browser-smoke.sh` and `browser-smoke.mjs`: rendered page, accessibility and
  interaction checks without Docker. Exercises long refs, commit subjects and
  paths at 320, 390, 768, 1024 and 1440px, checking mobile card content and
  controls for clipping and dialogs for overflow. Runs remain queued.
  Default port: 18081.
- `backup-test.sh`: PostgreSQL/Git snapshot and restore checks, without Docker.
  Requires compatible `pg_dump` and `pg_restore` clients. Checks restricted
  reader grants and revocation, token scope, decrypted secret fingerprints,
  completed run/step output, summaries, deployments and pinned historical source.
  Completed history is seeded directly; execution is covered by `e2e.sh`.
  The restore test also rejects a mismatched secret key before writing any data.
  `backup-check` compares secret fingerprints through the repository service
  before and after restore without printing plaintext.
- `compose-smoke.sh`: boots the actual production Compose definition in a unique
  temporary Compose project, with its own named DB volume, host data directory,
  temporary `.env`, ports and image. It checks web readiness, installed binary,
  migration idempotence, and persistence across container recreation; then it tears everything down. Requires Docker Compose
  and free ports 18085/15433 (override with `GITMAN_COMPOSE_SMOKE_HTTP_PORT`
  and `GITMAN_COMPOSE_SMOKE_DB_PORT`). Never points at a user's `.env`.
- `verify-release-artifacts.sh`: before publishing, check the four expected
  distributables, complete checksums, archive contents, gzip integrity and
  the `amd64` binary version. The release workflow runs `release-artifacts-test.sh` before publishing to
  check tampering and accidental source secrets without Docker or PostgreSQL.
- `verify.sh`: combined project checks, for source verification;
  see `Makefile` for individual targets. CI sets `GITMAN_REQUIRE_SHELLCHECK=1`
  and installs ShellCheck so missing shell lint cannot be silently skipped.
- `check-js-syntax.sh`: syntax-check all checked-in browser JavaScript and Node test harnesses with Node.js; no npm install required.

Install browser dependencies once:

```sh
(cd scripts && npm ci && npx playwright install chromium)
scripts/browser-smoke.sh
```

Use `PLAYWRIGHT_EXECUTABLE_PATH` for an existing Chromium.
`GITMAN_BROWSER_PORT` and `GITMAN_E2E_PORT` choose unused HTTP ports.
Execution checks require a Docker daemon with `alpine:3.20` already available;
neither Gitman nor these scripts pull it automatically. Docker checks only touch
step containers labelled with the disposable instance's identity.
