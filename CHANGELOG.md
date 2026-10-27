# Changelog

What changed in each release, for the people who run Gitman. Newest first.
Each release's section is also its GitHub release notes.

## Unreleased

### Fixed

- A worker now reads `docker system df` the way Docker 29 prints it, with every
  column as a string. In beta 24 the read failed, so no image inventory was
  published and the build-cache cap was never enforced.

## [1.0.0-beta.24] - 2026-10-27

Image inventory, retention and build-cache management.

**Installation:** this release adds migration `0005_images.sql`, which creates
the per-host image inventory and extends the operations journal for
host-targeted image work. Existing databases upgrade in place when `web` or
`worker` starts. Pipelines that build images should declare them under
`images:`; an undeclared image is still inventoried but is never pruned
automatically. A server running an earlier beta rejects `images:`, so upgrade
Gitman before adding it to pipelines.

The Gitman image is now built on Alpine. A host that builds it needs
`golang:1.27-alpine` and `alpine:3.24` locally in place of
`golang:1.27-bookworm` and `debian:bookworm-slim`, and the `DEBIAN_MIRROR` and
`DEBIAN_SECURITY_MIRROR` build arguments are replaced by `APK_MIRROR`.

### Added

- **Image inventory and retention**: pipelines declare built images under
  `images:` with an explicit `keep` of 1–50. Workers publish each Docker host's
  inventory; the admin **Images** page and `gitman admin images list` show
  required, built and other images with their reason, size, age, in-use and
  deployed state and whether they are prunable. Required images, in-use images
  and the deployed version are protected. `gitman admin images prune` and
  `images remove` are dry runs unless given `--confirm`, then queue durable
  work for the owning worker.
- **Missing-image checks**: `gitman admin images missing` lists required images
  no ready worker advertises, with the pipelines that require them, and exits
  non-zero when anything is missing. The Images page shows the same warning.
- **Image automation**: `GITMAN_IMAGE_AUTOMATION` selects `off` (the default),
  `after-deploy` or `low-disk`. Automatic pruning removes only built images
  beyond their keep count.
- **Bounded build cache**: workers enforce a 3 GiB / 7 day cache policy by
  default after Docker runs and periodically, using `docker builder prune
  --all` with a storage cap and an age filter. It is tunable with `GITMAN_IMAGE_CACHE_KEEP_GIB`,
  `GITMAN_IMAGE_CACHE_MAX_AGE_HOURS` and `GITMAN_IMAGE_CACHE_ENABLED`, and
  manual `admin images prune --cache` uses the same path. Inventory and the
  Images page show cache size, policy, last run and reclaimed bytes.
- `gitman admin images list --json` for scripts.

### Changed

- **Smaller image on Alpine**: the runtime is `alpine:3.24` with `git`,
  `curl`, `ca-certificates` and `tzdata`, and the builder is
  `golang:1.27-alpine`. The binary is built with `CGO_ENABLED=0` and the Docker
  client is the Docker project's static binary, so nothing depends on glibc.
  The user and group are still UID/GID 1000.
- `repository_operations` now also carries host-targeted image operations;
  **Operations** shows both repository recovery and image work, and image
  removal/cache results are per image.
- Worker readiness still advertises available images; the Images page is the
  full inventory, not just that claim-routing list.
- Documentation for Docker disk use, pipeline `images:`, the CLI, settings,
  security bounds and troubleshooting now covers image retention and the build
  cache. The "no caches" wording now distinguishes Gitman caches from the
  worker's local BuildKit cache.

## [1.0.0-beta.23] - 2026-10-27

The last beta before 1.0: security and stability fixes, repository maintenance,
session management, and a read-only diagnostic command.

**Installation:** this release adds ordinary schema migrations (session
details, activity visibility and two indexes). Existing beta 22 databases
upgrade in place when `web` or `worker` starts.

### Added

- **Repository maintenance**: a Maintenance tab on a repository's Settings
  page shows its disk usage and runs `git gc`. `gitman admin repo size` and
  `gitman admin repo gc` do the same from the command line, and `repo list`
  shows each repository's size. Pushes wait for maintenance instead of
  interleaving with it.
- **Sessions**: the account page lists every signed-in browser with its
  address, device, last-seen time and expiry, marks the current one, and can
  revoke each. Admins get a **Sessions** page beside **People**; admin
  revocations are recorded in Activity. `gitman admin session list` and
  `session revoke` expose the same, and `gitman admin token list` shows a
  person's tokens and their last use.
- **`gitman doctor`**: a read-only check of config, data directory and free
  space, database and schema version, Docker and registered workers. It is safe
  beside a live web or worker; warnings exit 0, failures exit 1.
- `gitman admin worker release --confirm <worker-id>` releases the bookkeeping
  of a worker whose host is gone for good, after the operator confirms its
  containers are stopped.
- `gitman admin operation abandon --reason TEXT <id>` releases a repository
  whose operation recovery has failed, when the operation has no Git receipt
  proving it changed Git. It is audited and leaves Git and database rows for
  inspection.
- Compose memory limits for web, worker, database and proxy, log rotation on
  every service, a Caddy health check, and an explicit worker environment that
  no longer receives the whole `.env` file.

### Changed

- Sign-in admission moved behind body reading and authentication, with login and
  logout exempt, so stalled unauthenticated requests can no longer take every
  maintenance slot. Mutation admission now waits at most three seconds before
  answering 503.
- Login attempts are reserved before bcrypt runs, IPv6 clients are counted by
  /64, and one username is limited across addresses as well as per address.
  Wrong current-password checks on the account page are limited too.
- A worker fetching a run's commit can use capacity reserved from ordinary Git
  requests, so a burst of clones cannot fail a run with a 503. Git slots are
  taken after authentication.
- A binary refuses to start against a database schema newer than the migrations
  it embeds, and reports pending or ahead migrations to `doctor`.
- Git pin reconciliation has its own per-repository budget, accepts a much
  larger audit window, and runs `gc --auto` outside the repository mutation
  lock. Retention phases now run independently with separate budgets.
- Stale repository trash is swept hourly, not only at startup. A failed orphan
  quarantine is logged instead of stopping web.
- Admin-only person and repository-deletion events no longer appear in every
  member's Home feed. Deleting a restricted repository no longer announces its
  name to members.
- The `field` template disables autocapitalisation and spellcheck for text
  inputs, the copy button works on plain-HTTP installs, and a dialog no longer
  closes when a drag starts inside it and ends on the backdrop. Token creation
  offers 180- and 365-day expiries, matching the documented maximum.
- Pipeline timeout errors explain that a unit is required (`20m`, not `20`), and
  `gitman check` warns when Docker or deploy targets need a ref rule.
- Panel scrolling: the wheel now scrolls a dashboard panel when it is over a
  table cell, not only over the gaps between cells. On phones, the run log no
  longer traps page scroll at its end.
- Documentation now covers upgrades, Docker image pruning and backup tooling,
  pipeline limits, fixed settings, trusted-proxy lockout, HSTS, session and
  token revocation, and the remaining secret-masking limits. The release promise
  is an in-place upgrade from any 1.0 release candidate to 1.0.

### Fixed

- A disk check that outlives its five-second walk no longer kills a run with a
  misleading "worker was shut down" reason; the tick is skipped, and a real
  budget breach still stops it.
- Expired access tokens are pruned with expired sessions.
- Worker readiness now uses one one-minute threshold everywhere, while a run is
  failed only after five minutes without a heartbeat.
- `admin worker list`, which never existed, is gone from the docs; cleanup and
  release are the worker commands.
- The SQL comment claiming all Git reads precede a push transaction was
  corrected; ref indexing still reads Git inside it.
- Pending-operation checks and retention scans use new indexes for pending
  logs and completed operations.
- Generated-password responses are documented as one-shot: refreshing the page
  re-submits the action.

Known gaps are listed in [ROADMAP.md](ROADMAP.md#later): no TTL for queued runs
with images no worker has, retention does not truncate stored pipeline bytes,
and CI does not start the full Compose stack.

## [1.0.0-beta.22] - 2026-10-02

Refs and history: managing branches, tags and revisions from the web.

**Installation:** this release uses a new database baseline. It does not
automatically upgrade an existing beta 21 database.

### Added

- **Commits** is a section of its own: a branch's, tag's or commit's
  commits, filterable to a path from the file browser, with each commit's
  run, the branches and tags at it and a merge marker. Choosing a ref
  changes the list in place.
- Compare any two branches, tags or commits on the same page: choose what
  to look at and what to compare it with, and see how far apart they are,
  the commits between, and the files they change, in two tabs: the commits,
  and one list of changed files that each open to their diff, with Expand
  all and Collapse all. Two tags show what a release added. Each deployed
  target on the Overview counts the commits it has not shipped and links to
  them, and each branch says how far it is ahead of and behind the default
  branch.
- The Overview lists what is deployed and how many commits each target has
  not shipped, and strips the default branch's latest runs. Home lists every
  repository with the latest commit and run of its default branch, and what
  is deployed where, beside a small **Needs attention** card that appears only
  when a default branch's run failed, runs wait for a worker, a target is
  behind, or a push was refused this week.
- **Activity** for each repository: every ref change (created, pushed,
  force-pushed, deleted, tag moved), who made it and the run it started,
  with runs, what was shipped and settings changes. The Overview shows the
  newest and links to all of it. Pushes from before this release show a
  force-push as a plain push.
- A push Gitman refuses is recorded, and shows in Activity with who pushed
  and the reason for each ref. The lines Git prints to the pusher are
  unchanged.
- Delete a branch or tag from its row on the Overview. The button shows
  only where a push deleting it would be accepted, it is checked again on
  the server, and a branch's confirmation says whether it is merged into
  the default branch. It is recorded like a pushed delete, so it shows in
  Activity.
- Download a branch, tag or commit as a `.tar.gz`, from **Download** next
  to the ref picker on Files; the `.zip` is at `/<repo>/archive/<ref>.zip`.
  It is streamed from `git archive`, follows the repository's read access,
  and shares the limit on concurrent Git requests.

### Changed

- Let mobile metadata cards wrap their full content and controls, keep panel
  headings and toolbars within narrow screens, and make dialog forms scroll
  within the available height. Desktop dashboard sizes and compact rows remain.
- Keep the self-hosted tag pipeline focused on building the image, smoke-checking
  its embedded version, and reporting its image and version. Full verification runs
  in GitHub Actions.
- Remove bundled PostgreSQL tools and the PostgreSQL image build dependency.
  Backup and restore use tools installed on the host.
- Backup integration checks restored reader grants and revocation, token scope,
  secret values, completed logs and deployments, and pinned historical source.
- Check theme radio states and accessibility with the account menu open.
- Remove the session-specific implementation checklist from the documentation.

- A theme switch in the account menu: the system's theme, light or dark,
  kept in the browser.
- Files is no longer in the repository's nav, and has no Code and History
  tabs: the file browser is a step down from the Overview, from a ref's name,
  **Browse files**, or a commit. **Commits** on a file or
  directory lists the commits that changed it, and old `?tab=history`
  addresses redirect there. Comparing two refs moved onto Commits, and
  `/compare/<base>...<head>` addresses redirect there. The Overview lists
  branches and tags one under the other, not as tabs.
- The Runs table shows each run's commit, who started it, how long it
  took, and the target it shipped to.
- Tables show only the columns that have something in them: a repository
  with no runs has no run columns on its Overview, and the commit list has
  no Run column until a commit has one.
- Tags are listed newest version first, in the Overview and in the
  pickers. A push that creates, moves or deletes many refs at once is one
  line of Activity that names the newest and counts the rest.
- Home and the Overview fit the window: their panels share its height and
  scroll inside themselves, whether empty or full. Every page's tables
  and panels have a titled header with their action beside it, and Commits
  and Runs no longer repeat the active tab as a heading.
- Home no longer has a Running now panel: a run in progress shows on its
  repository's row.
- The Run dialog chooses its branch or tag with the same searchable list as
  Commits. The activity feed and the page itself no longer both scroll.
- A deployment shows its version only when it isn't its commit. A branch
  deploy no longer shows the same commit twice on the Overview and Home.

### Fixed

- Give repository recovery a full execution budget after acquiring its mutation
  lock, so a recoverable push cannot be wedged by the short lock-wait timeout.
- Retry recording a run's end without rewriting its valid summary after transient
  database failures.
- Give the serialized race-test suite enough time for production-cost password
  hashing and integration-heavy web tests.
- Use PostgreSQL 18 consistently in the default Compose setup, CI and release
  integration checks.
- Reject stale ref updates and unreadable commit metadata before recording push
  intent. Recovery closes unapplied ref conflicts so later mutations can proceed.
- Index nested annotated tags by their fully resolved commit, and normalize NUL
  and invalid UTF-8 in metadata stored in PostgreSQL.
- Preserve full Docker container IDs during receipt recovery and cleanup.
- Allow build output to disappear during disk monitoring without cancelling the
  run or reporting a false budget violation.
- Keep integration evidence separate from release downloads and checksums.

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
