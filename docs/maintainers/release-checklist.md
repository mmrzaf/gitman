# Release checklist

Releases are cut by pushing a tag matching `v*` (e.g. `v1.0.0`,
`v1.0.0-beta.1`); `.github/workflows/release.yml` does the rest.

1. On `develop` (or the branch you're releasing from), make sure `main`
   is up to date and `go.mod`'s `go` directive matches the Go version you
   intend to ship with.
2. Run the local verification set before tagging:
   ```sh
   make verify
   ```
3. Tag and push:
   ```sh
   git tag v1.2.3
   git push origin v1.2.3
   ```
4. The `release` workflow runs, in order:
   - **release-metadata** — validates the tag looks like `v1.2.3` or
     `v1.2.3-beta.4` and derives whether it's a prerelease.
   - **verify** — checks out the tag, confirms the Go toolchain matches
     `GO_VERSION`, runs `gofmt`, `go vet`, `golangci-lint`, `govulncheck`,
     the race-detector test suite against a real PostgreSQL service
     container, a smoke-build of the binary (`gitman version` must print
     `gitman <tag>`), and a Docker smoke build.
   - **build-binaries** — `linux/amd64` and `linux/arm64`, `CGO_ENABLED=0`,
     uploaded with checksums.
   - **source-archive** — `make release-source`, a tracked-files-only
     tarball (see [`scripts/release-source-archive.sh`](../../scripts/release-source-archive.sh)).
   - **docker-image** — a multi-arch (`amd64`+`arm64`) image pushed to
     `ghcr.io/<repo>`, tagged with the version, `major.minor`, and
     `latest` (skipped for a prerelease).
   - **docker-archive** — a `linux/amd64`-only image saved as a
     downloadable `.tar.gz`, for offline installs.
   - **create-release** — publishes a GitHub Release with every artifact
     above plus an aggregate `SHA256SUMS`, and generated release notes.
5. To republish an existing tag (for example after a workflow-only fix),
   use the workflow's `workflow_dispatch` input instead of re-tagging.
6. Verify after the workflow completes:
   - The GHCR image pulls and runs: `docker run --rm ghcr.io/<repo>:1.2.3 gitman version`.
   - The GitHub Release has all expected assets and a `SHA256SUMS` file.

## Notes

- The release workflow builds with the public `GOPROXY` and no Alpine
  mirror — those are for restricted-network *self-hosted* pipeline runs
  (see this repository's own [`.gitman.yml`](../../.gitman.yml)), not for
  GitHub-hosted runners.
- There's no data migration between major Gitman versions with different
  storage backends (e.g. the move from SQLite to PostgreSQL) — that's a
  fresh install, not an upgrade. Say so in the release notes if a release
  changes the storage backend.
