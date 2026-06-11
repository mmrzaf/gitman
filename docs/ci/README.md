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

Run statuses are `pending`, `running`, `success`, `failed`, and `skipped`.

## Automatic triggers

Repository owners can install an auto-trigger hook from the **CI/CD** page. The generated bare-repository `post-receive` hook submits one push event for each updated branch or tag. Deleted refs are ignored. Hook delivery failures do not fail the Git push.

## Start authoring

Read [CI configuration](configuration.md), then [secrets](secrets.md) and [artifacts](artifacts.md).
