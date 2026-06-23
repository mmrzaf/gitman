# Gitman CI

Gitman CI is a deliberately small Docker-backed pipeline runner. A repository defines one pipeline in a root-level `.gitman-ci.yml`. The worker clones the selected commit, validates the file, generates a shell script, and runs it in a restricted container.

## Use Gitman CI for

- Tests and build checks for small teams.
- Producing downloadable build artifacts.
- Reusing a repository-scoped cache.
- Isolated runner hosts with a curated set of pre-pulled images.

## Do not treat it as

- A hardened untrusted multi-tenant sandbox.
- A replacement for a dedicated runner fleet.
- A place to run jobs from arbitrary public contributors on a shared production host.

## Pipeline lifecycle

1. A manual action or repository post-receive hook creates a pending run.
2. A worker claims the run and creates an attempt-scoped lease.
3. The worker clones the selected commit into a temporary workspace.
4. Missing `.gitman-ci.yml` marks the run as `skipped`.
5. A valid configuration is executed through `/bin/sh` inside the selected image.
6. Files written under `/gitman/artifacts` are collected.
7. The run becomes `success` or `failed`; logs remain available to repository members.

Run statuses are `pending`, `running`, `success`, `failed`, `skipped`, and `cancelled`.

## Manual runs and refs

The CI page can run the repository default branch, any existing branch, a tag, or a reachable historical commit. Gitman always checks out the exact selected commit and reads `.gitman-ci.yml` from that same commit. It does not support loading pipeline configuration from a different branch.

## Automatic triggers

Repository owners can install an auto-trigger hook from the **CI/CD** page. The generated bare-repository `post-receive` hook submits one push event for each updated branch or tag. Deleted refs are ignored. Hook delivery failures do not fail the Git push.

Automatic runs are trusted-ref aware. By default, only the repository default branch auto-runs. Other branches and tags can still be run manually, but push-triggered runs for them are ignored until the owner adds a CI ref trust rule. Rules may be exact refs or glob patterns such as `v*` for version tags and `release/*` for release branches.

Rapid pushes use latest-pending-push-wins semantics for each exact branch or tag. Older pending push runs for the same ref become `cancelled` with a visible reason. Manual runs and already-running jobs are not cancelled.

## Trusted refs, secrets, and Docker socket

Default policy when no matching rule exists:

| Ref | Manual run | Automatic push run | Secrets | Docker socket |
| --- | ---: | ---: | ---: | ---: |
| Default branch | Allowed | Allowed | Allowed | Denied |
| Other branch | Allowed | Denied | Denied | Denied |
| Tag | Allowed | Denied | Denied | Denied |

Repository owners can add exact branch or tag rules, or glob pattern rules, on the CI page. A rule can enable auto-run, CI secrets, and Docker socket trust for matching refs. Exact rules win over pattern rules. Stale exact rules remain visible and deletable if the Git ref is later removed.

If a pipeline requests a secret on an untrusted ref, the worker fails before decrypting repository secrets. If `.gitman-ci.yml` sets `docker: true`, both `GITMAN_CI_ALLOW_DOCKER_SOCKET=true` and matching Docker approval are required.

## Hook ownership

Gitman only manages `hooks/post-receive` when the file is absent or contains the Gitman marker. It refuses to overwrite or delete unmanaged hooks. Reinstalling a managed hook rotates the webhook token. Delivery failures are logged locally with `logger` when available and never print credentials.

## Start authoring

Read [CI configuration](configuration.md), then [secrets](secrets.md) and [artifacts](artifacts.md).
