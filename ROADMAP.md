# Roadmap

What's planned for Gitman, and why. Tick an item off when it ships, and
move it into [CHANGELOG.md](CHANGELOG.md) with its release.

## Beta 23

Meant to be the last beta: nothing new in scope, only what stops a
release candidate from being called one.

- [x] **Fix panel scrolling.** Cells are no longer wheel traps; a browser
  test wheels over a fitted panel's cell and asserts the panel scrolls.
- [x] **Decide the step-limit gap.** Step limits bound the step container,
  not a `docker build` or socket-created containers; documented in
  [docs/operator/security.md](docs/operator/security.md).
- [x] **Decide the settings people miss.** Default run timeout (30m), one run
  per worker, and the seven-day session are fixed and documented, rather than
  adding settings no real deployment needed.
- [x] **Pin the schema.** A golden schema test catches unintended schema drift;
  the upgrade promise is in
  [docs/operator/backups-and-upgrades.md](docs/operator/backups-and-upgrades.md),
  and a binary refuses a schema newer than itself.
- [ ] **Rehearse backup, restore and upgrade** on a copy of the server's
  data: back up, restore into a fresh host, bring up beta 23, check
  history, logs, secrets and deployments. Not run here: it needs the
  owner's real data and a fresh host.
- [ ] **Run the real pipelines.** Push cerv, sms-gateway and waiotech
  (`testdata/pipelines`) to a test instance and check each run and
  deploy end to end. Not run here: no Docker daemon or test instance.
- [x] **Read the docs against the code.** Every `GITMAN_*` variable, CLI
  command and flag in docs/ matches the code; stale `worker list`,
  `POSTGRES_IMAGE`, missing-image and readiness text is corrected.
- [x] **Security pass.** Restricted repositories, live streams, archives and
  admin-only events no longer leak names; admission and sign-in limits no longer
  starve on unauthenticated bodies; secret masking now normalises terminal
  escapes first; docs state the remaining exceptions.
- [ ] **Browser and mobile pass** over every page in light and dark,
  at phone width and at the dashboard breakpoints (1000px, 560px height).
  The scrolling regression test is added; the full manual pass is not run
  here.
- [x] **Clear the host work below** that blocks real use: image-pruning
  guidance and the waiotech timeout note are documented, but the host actions
  themselves still belong to the owner.
- [ ] **Cut the release.** Move Unreleased into `## [1.0.0-beta.23]`,
  follow [docs/maintainers/release-checklist.md](docs/maintainers/release-checklist.md),
  then freeze: from the RC only fixes go in.

## Later

Found during the beta 23 audit; each needs a design or a decision, not a
last-minute patch.

- [ ] **Bound queued runs that no worker can ever claim.** A run whose
  required images no worker has stays queued forever; there is no TTL
  (`internal/ci/store.go`, `claimNextRun`). Decide whether to fail it after a
  deadline, notify an admin, or both.
- [ ] **Retention for stored pipeline bytes and push payloads.** `runs.pipeline`
  and `repository_operations.payload` stay for the full audit window even after
  the run or operation finishes. Retention prunes the surrounding rows but not
  these columns.
- [ ] **A push refused after its refs were applied.** If `recordPush` times out
  after `ApplyOperation` succeeded, proc-receive answers `ng`; recovery can
  still record the push later (`internal/push/proc.go`, `recoverPush`). A clear
  "applied, recording late" answer needs a protocol-level design.
- [ ] **Leftover `objects/incoming-*` after a killed receive-pack.** Suspected,
  not reproduced: a killed receive-pack may leave a quarantine directory that
  nothing sweeps. Reproduce with a kill during a large push before adding
  cleanup.
- [ ] **Pagination for repositories with thousands of refs.** Overview and Home
  do per-ref work and share a 15-second page budget; the budget bounds the
  damage but a very large repository can still exceed it.
- [ ] **Bound raw file downloads.** They take no Git slot and have no size cap
  beyond what Git serves (`internal/web/browse.go`).
- [ ] **Track containers a `docker: true` step starts itself.** Gitman only
  cleans up step containers it created; child containers are the operator's.
  Documented in the security model rather than guessed at by cleanup.
- [ ] **CI never starts the Compose stack.** CI runs `docker compose config`
  and the real-Docker integration script, but not `up`. Arm64 images are built
  only at release. An inexpensive job could cover the compose health path.
- [ ] **Replace the remaining sleep-based tests** with polling or channels
  (`internal/worker/worker_test.go`, `logs_test.go`, `internal/web/live_test.go`,
  `run_test.go`, `internal/git/timeout_test.go`). The worst timed waits were
  left because they test real timer behavior, not readiness.

## Pipelines on the server

Not Gitman changes, but open work on the projects it runs.

- [ ] waiotech: add `timeout: 60m`, so a first, uncached build can't be
  stopped mid-deploy by the 30-minute default. Documented in the pipeline
  guidance; the real pipeline is not changed here.
- [ ] waiotech: tick **run** on the `develop` rule, so pushes deploy
  staging.
- [x] Prune old images on the host: pipelines declare built images under
  `images:`, the Images page and `admin images` classify and prune them, and
  workers bound the BuildKit cache by default. See
  [Docker deployment](docs/operator/docker.md#disk-use-and-images).
- [ ] cerv: tag `cerv:1.0` from a `cerv:1.0.x` build, since waiotech's
  pipeline requires it.

## Postponed

Not now, but not ruled out. The README currently lists both as not added.

- [ ] **Pipeline artifacts.**
- [ ] **SSH.**

## Decided against

Things Gitman leaves out on purpose are listed in the README's [Not
added, on purpose](README.md#not-added-on-purpose); an idea there needs a
new reason before it comes back here.
