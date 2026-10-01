# Development

## Prerequisites

Go 1.27 and Git. Most of the test suite needs a reachable PostgreSQL;
tests that need it skip themselves when `GITMAN_TEST_DATABASE_URL` isn't
set.

## Building and testing

```sh
go build ./...
go test ./...        # DB-dependent tests self-skip without a database
```

To run every test, including the DB-dependent ones:

```sh
export GITMAN_TEST_DATABASE_URL='postgres://postgres@localhost/gitman_test?sslmode=disable'
go test -p 1 -count=1 ./...     # isolated schemas; limits CPU use
```

`make verify` runs the same local checks the release pipeline does:
formatting, shell syntax, tests with the race detector, vet, pinned lint and vulnerability scans, and a build with
a version smoke check. See the [`Makefile`](../Makefile) for the full
target list (`build`, `build-all`, `test`, `test-coverage`, `lint`,
`fmt`, `deps`, `release-source`).

## End-to-end and browser tests

`scripts/e2e.sh` builds the binary and drives it against a real
PostgreSQL and Docker daemon, covering pipeline runs, cancellation, and
worker-crash cleanup. `scripts/browser.sh` (via `scripts/browser.mjs`)
drives the web UI with Playwright and axe-core for functional,
accessibility (WCAG 2.1 A/AA), and responsive checks. Both need their
own runtime (a real Postgres/Docker for the former, Node.js and the
dependencies in `scripts/package.json` for the latter) and are not part
of `go test`.

## Linting

`.golangci.yml` enables `errcheck`, `govet`, `staticcheck`, `ineffassign`
and `unused`. Run it locally with `make lint` or `golangci-lint run`.

## Validating a pipeline file

```sh
gitman check .gitman.yml
```

See [Pipeline configuration](ci/configuration.md) for the schema.

Integration scripts create unique databases ending in `_test` and unique temporary
directories. They stop only their own processes, drop only the database they
created, and retain logs/screenshots for inspection. CI runs backup restoration,
real Docker execution and browser checks and uploads their evidence. Set
`GITMAN_INTEGRATION_ARTIFACTS` to copy those artifacts to a chosen directory.
Use `npm ci --prefix scripts` and `scripts/node_modules/.bin/playwright install chromium`
for the pinned browser dependencies. The in-app Browser is preferred when its
runtime is available; these repository scripts are the reproducible CI fallback.

The backup round trip seeds completed run history without Docker, replaces the
public branch history, and verifies that retained source, logs, summaries,
deployments, reader grants and secret values survive restoration. It also checks
reader revocation and repository-scoped token denial after restore. Pipeline
execution and container recovery are covered separately by the Docker checks.
