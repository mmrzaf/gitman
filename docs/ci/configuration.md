# Pipeline configuration (`.gitman.yml`)

```yaml
image: docker:29-cli           # every step runs in this image
docker: true                   # steps use the host's Docker; a ref rule must allow it
timeout: 20m                   # the whole run; default 30m, at most 24h
requires:                      # images that must already be on the host
  - golang:1.27-bookworm
  - debian:bookworm-slim
env:                           # for every step
  GOPROXY: https://goproxy.example.com,direct

targets:                       # where a ref ships to
  staging:
    branch: develop
    env:
      DEPLOY_DIR: /srv/apps/waiotech-stage
  production:
    tag: "v*"
    env:
      DEPLOY_DIR: /srv/apps/waiotech

steps:
  - name: build
    run: docker build --pull=false --build-arg GOPROXY="$GOPROXY" -t "waiotech:$GITMAN_VERSION" .
  - name: check
    run: docker run --rm "waiotech:$GITMAN_VERSION" waiotech version
  - name: deploy
    when: target               # always (default), target, branch, tag, or a target's name
    run: ./deploy.sh "$DEPLOY_DIR" "waiotech:$GITMAN_VERSION"
```

A pipeline is meant to stay this light: build, check, and deploy. Each
step is one container; Gitman starts no databases or other services
next to it, so tests that need them belong in a CI that has them.

## Fields

- **`image`** (required) — the Docker image every step runs in.
- **`docker`** — when `true`, steps get the host's Docker socket. This is
  root on the worker's host; a ref rule must separately allow it
  (`--docker` on `gitman admin rule set`) before a run on that ref can use
  it. See [Security model](../operator/security.md).
- **`timeout`** — the whole run's limit, not per step. Defaults to 30
  minutes; at most 24 hours.
- **`requires`** — images that must already be on the worker's host,
  such as the base images a `docker build --pull=false` uses. A run
  checks them, and `image`, before its first step, and fails naming any
  that is missing. Gitman never pulls them, and starts nothing from
  them.
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
- `GITMAN_VERSION` — for a tag, its name with anything but letters, digits,
  `.`, `-` and `_` replaced by `-`; for a branch, the commit's first 12
  characters. It is safe as a Docker image tag.
- `GITMAN_SUMMARY` — a file path; lines of `key=value` appended to it
  appear on the run's page

## Checking a file

```sh
gitman check .gitman.yml
```
