# Development

## Prerequisites

Go 1.27.4 and Git. The module requires the 1.27.4 patch for GO-2026-6859; with
an older local toolchain set `GOTOOLCHAIN=auto` so Go downloads the pinned
toolchain. Most of the test suite needs a reachable PostgreSQL;
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
go test -count=1 ./...     # each test gets its own schema
```

`make verify` runs the same source checks the release pipeline does:
formatting, shell and JavaScript syntax, tests with the race detector, vet, pinned lint and vulnerability scans, and a build with
a version smoke check. See the [`Makefile`](../Makefile) for the full
target list (`build`, `build-all`, `test`, `test-coverage`, `lint`,
`fmt`, `deps`, `release-source`).

### Native PostgreSQL tests

Use your installed PostgreSQL service; no Compose database or Docker container
is required for Go tests, authenticated Git transport or backup/restore tests.
Create a **dedicated disposable** database ending in `_test` and export:

```sh
export GITMAN_TEST_DATABASE_URL="postgresql://$(whoami)@/gitman_test?host=/var/run/postgresql&sslmode=disable"
go test -race -count=1 ./...
```

Host integration scripts create and remove their own test databases. They use
`PGHOST`, `PGPORT`, `PGUSER`, and optionally `PGPASSWORD`, instead of
`GITMAN_TEST_DATABASE_URL`. Run `scripts/clean-install-test.sh` and
`scripts/backup-test.sh` without Docker. Pipeline execution and the full
browser/worker suites do require Docker because pipelines are containers.

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

## Code organization

The Go architecture stays a small, package-oriented monolith. Keep run execution
and Docker host image management separate without creating additional packages:

- `internal/ci/service.go` owns run admission, lifecycle and read methods.
  `service_workers.go` handles worker registration, readiness, ownership and
  interrupted-container reconciliation; `service_images.go` handles image
  facts, host inventories and durable image operations.
- `internal/ci/store.go` contains run, step, log and deployment persistence.
  `store_images.go` contains required-image discovery, host inventory and
  image-operation persistence. Both share the existing PostgreSQL transaction
  interface and SQL schema.
- `internal/worker/docker.go` owns container execution, inspection and cleanup.
  `docker_images.go` owns Docker engine identity, host image inventory, image
  deletion and build-cache statistics/pruning. They deliberately share the
  same Docker CLI wrapper, timeouts and `Docker` type.
- Frontend code remains server-rendered HTML with modular JavaScript. Run
  `scripts/check-js-syntax.sh` (Node.js required, no npm dependencies) to
  validate checked-in browser scripts and Node acceptance harnesses; it also
  runs as part of `scripts/verify.sh` in both CI and tagged release verification.

Refactor within these responsibilities when a boundary helps readability;
avoid moving large files or introducing abstractions without a behavior test.

## Validating a pipeline file

```sh
gitman check .gitman.yml
```

See [Pipeline configuration](ci/configuration.md) for the schema.

Integration scripts create unique databases ending in `_test` and unique temporary
directories. They stop only their own processes, drop only the database they
created, and remove their temporary data on exit. CI runs backup restoration,
real Docker execution and browser checks and uploads their evidence. Set
`GITMAN_INTEGRATION_ARTIFACTS` to copy those artifacts to a chosen directory.
Use `npm ci --prefix scripts` and `scripts/node_modules/.bin/playwright install chromium`
for the pinned browser dependencies. The repository scripts provide reproducible browser verification.

The backup round trip seeds completed run history without Docker, replaces the
public branch history, and verifies that retained source, logs, summaries,
deployments, reader grants and secret values survive restoration. It also checks
reader revocation and repository-scoped token denial after restore. Pipeline
execution and container recovery are covered separately by the Docker checks.

## Interface acceptance for 1.0.0

The existing server-rendered templates remain the source of truth: JavaScript
is progressive enhancement, not a separate client-side application. Before
releasing, run `scripts/browser.sh` with real PostgreSQL and Docker, inspect
its desktop and phone-width screenshots in both themes, and review:

- Home with zero/many repositories, attention items and deployments; check
  that routine missing metadata does not look like a warning.
- Repository navigation on a phone, with the active Files/Settings tab visible
  and the overall page free from horizontal overflow.
- Full Activity entries with long descriptions (unlike compact sidebar feeds).
- Failed and running step logs with many lines; check error navigation, raw
  output, new-output follow, wrapping, deep links, cancellation and retention.
  Highlighting is a text search hint, not a definitive error diagnosis.
- People, tokens, secrets, and rules: use keyboard-only navigation and verify
  that role changes and destructive actions ask for confirmation. Refresh a
  credential-reveal page and confirm that it does not regenerate credentials.
- Browser back/forward, page reload, live refresh without stolen focus/scroll,
  no-JavaScript fallbacks, forced-colors, reduced-motion and narrow screens.

Browser tests are part of the release acceptance process; passing syntax checks
or isolated component tests alone does not certify the whole application.

## Release acceptance

CI and tagged releases run the same source, integration and browser checks.
The integration job additionally starts an isolated production Compose stack.
Artifact publication stages and verifies a draft before publishing the
versioned Docker image; floating tags update only after the release. See the
[release checklist](maintainers/release-checklist.md) and
[verification matrix](maintainers/verification-matrix.md).

A clean host, independent restore, and production-equivalent worker network
are required manual gates for 1.0.0. Source checks do not certify them.
