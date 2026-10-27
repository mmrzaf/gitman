# Pipeline configuration (`.gitman.yml`)

```yaml
image: docker:29-cli           # every step runs in this image
docker: true                   # steps use the host's Docker; a ref rule must allow it
timeout: 20m                   # the whole run; default 30m, at most 24h
requires:                      # images that must already be on the host
  - golang:1.27-bookworm
  - debian:bookworm-slim
images:                        # images this pipeline builds, and how many tags to keep
  example-app: {keep: 3}
env:                           # for every step
  GOPROXY: https://goproxy.example.com,direct

targets:                       # target contexts matched by ref
  staging:
    branch: develop
    env:
      DEPLOY_DIR: /srv/apps/example-app-stage
  production:
    tag: "v*"
    env:
      DEPLOY_DIR: /srv/apps/example-app

steps:
  - name: build
    run: docker build --pull=false --build-arg GOPROXY="$GOPROXY" -t "example-app:$GITMAN_VERSION" .
  - name: check
    run: docker run --rm "example-app:$GITMAN_VERSION" example-app version
  - name: deploy
    type: deploy
    when: target               # always (default), target, branch, tag, or a target's name
    run: ./deploy.sh "$DEPLOY_DIR" "example-app:$GITMAN_VERSION"
```

Each step runs in one container. Gitman does not provision service containers;
a pipeline with Docker permission can start its own test dependencies and must
clean them up. Deployment may be followed by health checks or other verification.

`images` is the pipeline's declaration of what it builds. Gitman uses it only
for inventory and retention on the worker host; it never builds an image,
finds one, or pulls one on its own. A repository named there must not also
appear in the same pipeline's `image` or `requires`.

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
  is claimed only by a worker advertising all of them, and `image`. A missing
  image leaves it queued; execution also rechecks images before its first step. Gitman never pulls them, and starts nothing from
  them.
- **`images`** — built images, as a map of Docker repository name to
  `keep`. The name is a repository only, with no tag or digest. `keep` is
  required and is between 1 and 50: for each repository, the worker keeps the
  newest `keep` tags by image creation time, plus any image a container uses,
  the version currently deployed, and any exact reference another pipeline
  requires. Everything else is prunable. Images a pipeline does not declare are
  never pruned automatically. See [Docker deployment](../operator/docker.md#disk-use-and-images)
  for how the worker classifies and prunes images.
- **`env`** — variables set for every step. Keys starting with `GITMAN_`
  are rejected — that prefix is reserved for the variables below.
- **`targets`** — a map of target names to ref matching and environment context. Each target matches
  either a `branch` or a `tag` pattern (`main`, `release/*`, `v*`). When a
  run's ref matches more than one target's pattern, the most specific
  pattern wins. A target's own `env` is merged over the pipeline's `env`
  only for a run that resolved to it.
- **`steps`** — run in order. The first failing step ends the run; later
  steps are skipped. Each step is:
  - **`name`** — shown on the run's page.
  - **`type`** — `run` (the default) or `deploy`. At most one deploy step is
    allowed; it must use `when: target` or a configured target name. Only a successful deploy
    step records a deployment, even when a later check fails.
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
gitman check [file]
```

It is syntax-only: a warning reminds you that `docker: true` or a target needs a
ref rule with `--docker` or `--deploy`. It does not contact a server or run
anything.

## A minimal pipeline

```yaml
image: alpine:3.20
steps:
  - name: test
    run: echo hello
```

A pipeline with no target starts no deploy; add a `targets:` map and a
`type: deploy` step only when you need managed deployments.

## Limits and fixed behavior

These are deliberately not tunable per repository:

- The pipeline file is at most 256 KiB; `run` scripts are at most 64 KiB each.
- At most 64 steps, 16 targets, 32 `requires` images, 32 built-image
  repositories and 128 environment variables per pipeline (8 KiB per value).
  Each built image's `keep` is 1–50.
- A run's `timeout` defaults to 30 minutes and may not exceed 24 hours. Write a
  unit: `20m`, not `20`.
- A worker runs one run at a time. Workers never pull images, so a run stays
  queued until a ready worker has all of them.
- There is no per-step timeout, no continue-on-error, and no step-level `env`.
  A step's shell script can implement a shorter timeout itself if it needs one.
- Secrets go to every step of a run whose ref rule allows secrets. A step that
  can print one can leak it; see [Secrets](secrets.md).

## Deployment ownership

An explicit deploy step that will run needs the ref rule’s **Managed deployment**
permission (`--deploy`). This enables target serialization and deployment history;
it does not prevent other steps from changing external systems through network
access, available credentials or a Docker socket. Trust everyone allowed to
change and run these scripts.
Build steps may use target environment variables without that permission.
Deploy scripts for the same repository and target run one at a time across all
workers; build steps still run concurrently. Ownership persists until termination
is confirmed. Lost heartbeats never expire ownership, and recovery does not
rerun deployment scripts. Check the Workers page and worker logs for pending cleanup.

Older automatic runs cannot deploy over a newer successful run of the same
target. A manual rerun remains available for an intentional rollback. Recovery
records a retained successful deploy receipt once, even if the worker died
before saving it. A subsequent health check may fail the run while leaving the
deployment recorded.
