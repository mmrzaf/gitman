# Integration checks

These scripts build a temporary binary and create a unique disposable PostgreSQL
database and working directory per invocation. They never drop an existing
database or reuse an installation's data directory. PostgreSQL connection
settings use `PGHOST`, `PGPORT` and `PGUSER` (defaults: localhost, 5432, postgres).
Logs and screenshots remain in the printed temporary artifacts directory.
`GITMAN_INTEGRATION_ARTIFACTS` optionally receives a copy of published evidence.

- `e2e.sh`: real Git pushes, Docker pipelines, cancellation, malicious input,
  raw-file security headers and recovery after a killed worker. The lost-worker
  fixture is deliberately aged past the five-minute threshold to avoid a long
  sleep; assertions check the resulting run state and container cleanup.
- `browser.sh` and `browser.mjs`: real Docker execution with live page updates,
  keyboard interactions, axe accessibility checks, mobile layouts, both themes,
  JavaScript and plain HTML fallbacks. Default port: 18081.
- `browser-smoke.sh` and `browser-smoke.mjs`: rendered page, accessibility and
  interaction checks without Docker. Runs remain queued. Default port: 18081.
- `backup-test.sh`: PostgreSQL/Git snapshot and restore checks, without Docker.
  Requires compatible `pg_dump` and `pg_restore` clients.
- `verify.sh`: combined project checks; see `Makefile` for individual targets.

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
