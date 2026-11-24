# Release verification matrix

This is the minimum evidence required for a stable release. A test's presence
in source does **not** mean that it passed on the release tag. The GitHub Actions
run and pilot report are the authority for results.

| Gate | Automated command / job | Acceptance | Separate host required? |
| --- | --- | --- | --- |
| Go source | `scripts/verify.sh` / source jobs | Formatting, lint, vuln scan, vet, race tests, versioned binary | No |
| Shell and frontend | `scripts/verify.sh` | ShellCheck required in CI, JS syntax, docs links | No |
| DB baseline | `scripts/clean-install-test.sh` | Empty DB, applied migrations, bootstrap, authenticated Git push | No |
| Git and jobs | `scripts/e2e.sh` | Real Git transport, Docker execution, cancellation, worker loss | No |
| Worker claims | `scripts/multiworker.sh` | Two workers on one host; never duplicate a run | **Yes**, for independent hosts |
| Secrets/recovery | `scripts/backup-test.sh` | Restore retains refs, pinned commits, secrets, grants, run/deploy/log history; wrong key refused | **Yes**, for a full disaster-recovery rehearsal |
| Compose | `scripts/compose-smoke.sh` | Isolated production stack builds, starts, migrates and responds ready | **Yes**, for target OS/host storage topology |
| Web | `scripts/browser.sh`, `scripts/browser-smoke.sh` | Chromium, keyboard, a11y, mobile, no-JS and long logs | **Yes**, for manual Firefox/Safari and operator workflows |
| Artifacts | `scripts/verify-release-artifacts.sh` | Four expected files; hashes; safe source; binary version; gzip integrity | **Yes**, for independent download/install |
| Offline image | Release draft staging job | Load Docker archive and run versioned binary | No |
| Registry | Versioned image job | Multi-arch manifest exists; versioned image pushed after draft | **Yes**, to verify pulls |
| Floating tag safety | Release workflow: `scripts/promote-floating-tags-test.sh` | Older releases cannot overwrite floating tags; API failure stops promotion | No |
| Publication | Draft/publish/promote jobs | Signed-off draft, released assets, stable aliases only after publication | No |

## Required manual failure rehearsals

On an isolated machine and with test repositories/pipelines, record the
observed outcome and recovery path for: database disconnection during a push,
worker loss during a build, worker loss during a deploy, Docker daemon outage,
full/read-only storage, restart with pending work, secrets with the wrong key,
restore of a damaged backup, and competing workers on **different Docker hosts**.
Do not introduce failure against a live server. Never automatically rerun an
interrupted deployment while its external effects are unknown.

## Environment

- Go 1.27.4, PostgreSQL 18, `pg_dump`/`pg_restore` v18, Git and Docker/Compose.
- Node.js 24; browser tests need pinned npm dependencies and Chromium.
- `GITMAN_TEST_DATABASE_URL` must reference an isolated `_test` database.
- All integration scripts create their own isolated databases and data folders.
- Required worker images are preloaded. Gitman itself does not pull pipeline
  images, so tests must prepare the Docker host explicitly.

The release candidate is *not* accepted solely because a browser screenshot
looks correct or a source parser finishes without errors. Actual CI, Docker,
PostgreSQL, restore and installed-artifact checks must pass.
