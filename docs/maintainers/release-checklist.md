# Release checklist

Releases are cut by pushing a tag matching `v*` (e.g. `v1.0.0`,
`v1.0.0-beta.1`); `.github/workflows/release.yml` does the rest.

1. On `develop` (or the branch you're releasing from), make sure `main`
   is up to date and `go.mod`'s `go` directive matches the Go version you
   intend to ship with.
2. Add the release's section to [`CHANGELOG.md`](../../CHANGELOG.md),
   headed `## [1.2.3] - YYYY-MM-DD`, and tick off what shipped in
   [`ROADMAP.md`](../../ROADMAP.md). The release workflow publishes that
   section as the release notes, and fails without one.
3. Run the local verification set before tagging:
   ```sh
   make verify
   ```
4. Tag and push:
   ```sh
   git tag v1.2.3
   git push origin v1.2.3
   ```
5. The `release` workflow runs, in order:
   - **release-metadata** — validates the tag looks like `v1.2.3` or
     `v1.2.3-beta.4` and derives whether it's a prerelease.
   - **verify** — checks out the tag, with the Go version `go.mod` names,
     and runs `gofmt`, `go vet`, `golangci-lint`, `govulncheck`, the
     race-detector test suite against a real PostgreSQL service
     container, a smoke-build of the binary (`gitman version` must print
     `gitman <tag>`), and a Docker smoke build.
   - **build-binaries** — `linux/amd64` and `linux/arm64`, `CGO_ENABLED=0`.
   - **source-archive** — `make release-source`, a tarball of the tagged
     commit (see [`scripts/release-source-archive.sh`](../../scripts/release-source-archive.sh)).
   - **docker-image** — a multi-arch (`amd64`+`arm64`) image pushed to
     `ghcr.io/<repo>`, tagged with the version, `major.minor`, and
     `latest` (skipped for a prerelease).
   - **docker-archive** — a `linux/amd64`-only image saved as a
     downloadable `.tar.gz`, for offline installs.
   - **create-release** — publishes a GitHub Release with every artifact
     above, their `SHA256SUMS` (check a download with
     `sha256sum -c --ignore-missing SHA256SUMS`), and its `CHANGELOG.md`
     section as the notes.
6. To republish an existing tag (for example after a workflow-only fix),
   use the workflow's `workflow_dispatch` input instead of re-tagging.
7. Verify after the workflow completes:
   - The GHCR image pulls and runs: `docker run --rm ghcr.io/<repo>:1.2.3 gitman version`.
   - The GitHub Release has all expected assets and a `SHA256SUMS` file.

## Notes

- The release workflow builds with the public `GOPROXY` and Debian
  mirrors. Mirrors are for restricted-network *self-hosted* pipeline runs
  (see this repository's own [`.gitman.yml`](../../.gitman.yml)), not for
  GitHub-hosted runners.
