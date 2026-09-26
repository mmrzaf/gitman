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
go test -p 1 -count=1 ./...     # one package at a time: DB tests share one database
```

`make verify` runs the same local checks the release pipeline does:
tests with the race detector, `go vet`, `golangci-lint`, and a build with
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
