# Changelog

What changed in each release, for the people who run Gitman. Newest first.
Each release's section is also its GitHub release notes.

## [Unreleased]

### Added

- History is a section of its own, between Files and Runs. **Commits**
  is a ref's log, filterable to a path, with each commit's run, the
  branches and tags at it and a merge marker. **Activity** is every ref
  change (created, pushed, force-pushed, deleted, tag moved), who made it
  and the run it started, with the repository's settings changes. Pushes
  from before this release show a force-push as a plain push.

### Changed

- Files no longer has Code and History tabs. **History** on a file or
  directory opens History filtered to that path, and old `?tab=history`
  addresses redirect there.

### Fixed

- A repository's clone address (`/<repo>.git`), opened in a browser, goes
  to the repository instead of a 404.

## [1.0.0-beta.21] - 2026-09-28

A rewrite. Gitman is rebuilt around PostgreSQL, Git over HTTPS and
Docker-based pipelines, keeping what beta 1–20 taught and dropping what
it didn't use.

**Upgrading:** there is no migration from beta 20. Its SQLite database and
repository layout are not read by this version: install fresh, recreate
people, repositories and rules, and push repositories again.

### Added

- PostgreSQL as the only data store. Web and workers coordinate through
  it alone, so any number of workers can run.
- Repository read access: visible to everyone signed in, or restricted to
  named readers and admins. A restricted repository is invisible to
  anyone else, in Gitman and over Git.
- Ref rules per branch or tag pattern: who may push, force-push and
  delete, and whether a push runs the pipeline, ships to a target, gets
  secrets and gets Docker. A default push policy covers refs no rule
  matches.
- Pipelines in `.gitman.yml`: steps in Docker containers, targets per
  branch or tag pattern, `when:` per step, `requires:` for images that must
  be on the host, a run summary, and deployment records shown on Home and
  each repository.
- Run a branch or tag by hand from the Runs page or its row on the
  Overview; Run again on any run, which on an older one rolls back.
- A push that starts no run says why, and a queued run says when no
  worker is online.
- A History tab on every file and directory; at the root, the branch's
  commits.
- An admin can change a repository's default branch; until it is pushed,
  Files and the Overview say so instead of showing a 404.
- Live pages: Home, the Overview and run pages update themselves, and a
  run's output streams.
- A command palette (Ctrl K) and a no-script Jump to page.
- Access tokens with read or read-and-write scope and an expiry, as the
  password for Git over HTTPS.
- `GITMAN_LOG_LEVEL` and `GITMAN_LOG_FORMAT`, and `GITMAN_TRUSTED_PROXIES`
  for client addresses behind Traefik and a CDN.
- Retention of finished runs (`GITMAN_RETENTION_DAYS`); each ref's latest
  run and every deployment are always kept.

### Changed

- Deployment is Docker Compose behind the host's Traefik, with PostgreSQL
  on the host's `data` network. The image is Debian-based, builds from
  mirrors given as build arguments, and needs no internet on the server;
  Gitman's own pipeline builds it on every `v*` tag.
- Every run is of a branch or tag. Running a bare commit, which no rule
  applied to, is gone.
- The web interface was redesigned for daily use: denser, keyboard-
  friendly, accessible, and light and dark.
- The web server's database pool and Git over HTTP concurrency are
  bounded, and answer `503` with `Retry-After` instead of hanging when
  busy.
- Stopping web with a page open no longer waits out the grace period,
  and a second Ctrl+C stops it at once.

### Removed

- SSH access, collaborators (replaced by readers and ref rules),
  self-registration, pipeline artifacts and caches, the audit page, the
  admin backup command (back up with `pg_dump`), and SQLite.
- Archive downloads, for now: see the [roadmap](ROADMAP.md).

### Security

- pgx v5.11.0 and golang.org/x/text v0.42.0, past GO-2026-5004 and
  GO-2026-5970.

## [1.0.0-beta.20] - 2026-09-11

### Changed

- Go 1.27 everywhere, and golangci-lint v2.13.2 for it.

## [1.0.0-beta.19] - 2026-09-11

### Changed

- CI builds use Liara's Debian and Go mirrors.
- GitHub Actions use Go 1.27.

## [1.0.0-beta.18] - 2026-09-10

### Added

- Access tokens with scopes, a finite expiry and last-used time.
- An audit trail of sign-ins, people, SSH keys, repositories and
  collaborators, and a security activity page that never shows secrets.
- Git over HTTP concurrency limits with `Retry-After`, and bounded
  repository browsing and diffs.
- Worker health, queue depth, storage admission and entry-count limits,
  and resource-limit and CI storage settings.
- Retrying a CI run.

### Fixed

- Sign-in throttling is per username and address.
- Repository settings are owner-only.
- Workers requeue and release leases cleanly when Docker is unavailable.
- The source archive tolerates tracked files deleted from the checkout.
- `GITMAN_GIT_RECEIVE_MAX_BYTES` is capped at 10 GiB.

## [1.0.0-beta.17] - 2026-08-18

### Added

- Inspectable pipeline runs, commit diffs and source navigation, in one
  unified repository interface.

### Changed

- Clear boundaries between the web, repository, CI and worker parts.
- Requires the patched Go 1.26.6 toolchain.

### Fixed

- Deterministic ordering of records with the same timestamp.
- Exact-run and durable-trigger semantics in CI.
- Diff rendering is bounded, and health probes are stable.

## [1.0.0-beta.16] - 2026-08-18

The first build of the beta 17 work, replaced the same day.

## [1.0.0-beta.15] - 2026-07-14

### Added

- Durable run controls and delivery: queued runs survive restarts and
  keep their order.
- Repository settings with guarded deletion.
- Continuous verification on GitHub.

### Changed

- Invalid runtime settings are rejected at startup.
- SSH keys must be canonical and unique.

## [1.0.0-beta.14] - 2026-06-23

### Added

- Glob patterns in trusted ref rules.
- A redesigned interface without Water.css or HTMX.

### Changed

- The Docker image is tagged from the Git tag.

### Fixed

- Runner failure logs, and the web manifest's icon paths.

## [1.0.0-beta.13] - 2026-06-23

### Changed

- The Docker image is based on Debian, so it builds from Debian mirrors.

## [1.0.0-beta.12] - 2026-06-23

### Added

- Downloading a CI run's full log.
- A favicon.

## [1.0.0-beta.11] - 2026-06-12

### Added

- Ref rules: only trusted refs run pipelines, and pushes cancel older
  queued runs.
- `gitman version`.

### Security

- Hardened Git over HTTP and sessions, bounded receive-pack input and
  safe hooks.
- A guard against unsafe files in the source archive.

## [1.0.0-beta.10] - 2026-06-11

### Fixed

- Rolled back a broken GitHub Actions change.

## [1.0.0-beta.9] - 2026-06-11

### Added

- Opt-in Docker socket access for pipelines, with its security boundary
  documented.
- Release builds are validated, and a Docker image archive is published.

### Fixed

- Startup retries transient SQLite locks.

## [1.0.0-beta.8] - 2026-06-10

### Fixed

- The self-hosted CI runner image, its Go proxy fallback, and running
  from the runner's tmpfs.
- The log keeps its scroll position while refreshing.

## [1.0.0-beta.7] - 2026-05-30

### Changed

- Pipelines are containerized YAML, replacing host scripts.
- A public URL setting and CI runner controls.

### Fixed

- Sessions, cookies, CSRF and sign-out; atomic migrations; consistent,
  symlink-safe backups; stricter ref resolution; atomic SSH key updates;
  long Git over HTTP transfers; attempt-scoped CI leases.

## [1.0.0-beta.6] - 2026-05-19

### Added

- Browsing tags, with ref validation.

### Fixed

- Archive downloads of refs with slashes; checking for `git` at startup;
  an 8-character minimum password; SSH key validation; expired sessions;
  artifact size limits; claiming each CI run exactly once.

## [1.0.0-beta.5] - 2026-05-15

### Fixed

- Git over HTTP in the Docker image.

## [1.0.0-beta.4] - 2026-05-15

### Fixed

- The release artifact name.

## [1.0.0-beta.3] - 2026-05-15

### Changed

- No Windows builds.

## [1.0.0-beta.2] - 2026-05-15

### Added

- CSRF protection, per-repository webhook secrets for CI, versioned
  database migrations, optional self-registration, and CI artifacts on
  the run page.
- Release builds for several platforms and a GHCR image.

## [1.0.0-beta.1] - 2026-05-07

The first release: repositories over Git over HTTP and SSH, a web
interface for files, commits and archive downloads, access tokens,
collaborators, host-script CI with secrets, artifacts and a worker, an
admin CLI with backups, and SQLite.
