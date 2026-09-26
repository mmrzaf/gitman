# Pipeline configuration (`.gitman.yml`)

```yaml
image: golang:1.27-alpine      # every step runs in this image
docker: false                  # true: steps can use the host's Docker
timeout: 20m                   # the whole run; default 30m, at most 24h
requires:                      # further images the steps use
  - postgres:16-alpine
env:                           # for every step
  CGO_ENABLED: "0"

targets:                       # where a ref ships to
  staging:
    branch: develop
  production:
    tag: "v*"
    env:
      DEPLOY_DIR: /srv/apps/waiotech

steps:
  - name: test
    run: go test ./...
  - name: release
    when: production           # always (default), target, branch, tag, or a target's name
    run: ./deploy.sh
```

## Fields

- **`image`** (required) — the Docker image every step runs in, unless a
  step needs one of the `requires` images as a sidecar.
- **`docker`** — when `true`, steps get the host's Docker socket. This is
  root on the worker's host; a ref rule must separately allow it
  (`--docker` on `gitman admin rule set`) before a run on that ref can use
  it. See [Security model](../operator/security.md).
- **`timeout`** — the whole run's limit, not per step. Defaults to 30
  minutes; at most 24 hours.
- **`requires`** — further images made available to steps, alongside
  `image`. None of these are pulled by Gitman; they must already exist on
  the worker's host.
- **`env`** — variables set for every step. Keys starting with `GITMAN_`
  are rejected — that prefix is reserved for the variables below.
- **`targets`** — a map of name to where a ref ships. Each target matches
  either a `branch` or a `tag` pattern (`main`, `release/*`, `v*`). When a
  run's ref matches more than one target's pattern, the most specific
  pattern wins. A target's own `env` is merged over the pipeline's `env`
  only for a run that resolved to it.
- **`steps`** — run in order. The first failing step ends the run; later
  steps are skipped. Each step is:
  - **`name`** — shown on the run's page.
  - **`when`** — `always` (the default), `target` (any target matched),
    `branch`, `tag`, or a target's own name (only that target matched).
  - **`run`** — the shell script for the step.

## Variables every step gets

- `GITMAN_REPO`, `GITMAN_RUN`, `GITMAN_COMMIT`, `GITMAN_SHORT`
- `GITMAN_REF`, `GITMAN_REF_KIND`, `GITMAN_TARGET`
- `GITMAN_VERSION` — the tag name for a tag ref, otherwise the short commit
- `GITMAN_SUMMARY` — a file path; lines of `key=value` appended to it
  appear on the run's page

## Checking a file

```sh
gitman check .gitman.yml
```
