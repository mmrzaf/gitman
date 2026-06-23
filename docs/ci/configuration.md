# CI configuration

Gitman reads `.gitman-ci.yml` from the repository root of the selected commit.

## Minimal example

```yaml
image: debian:bookworm-slim
steps:
  - name: verify
    run: |
      echo "Repository: $GITMAN_REPO"
      echo "Commit: $GITMAN_COMMIT"
```

## Full example

```yaml
image: golang:1.26-bookworm
env:
  APP_ENV: test
  GOMODCACHE: /gitman/cache/go/pkg/mod
  GOCACHE: /gitman/cache/go/build
  DEPLOY_TOKEN: ${{ secrets.DEPLOY_TOKEN }}
steps:
  - name: prepare cache
    run: |
      mkdir -p "$GOMODCACHE" "$GOCACHE"
  - name: test
    run: go test -coverprofile=coverage.out ./...
  - name: save report
    run: cp coverage.out /gitman/artifacts/coverage.out
```

This Go example assumes required modules are already present in the image or a warmed cache. The default CI network mode is `none`.

## Schema

| Key | Required | Type | Notes |
| --- | --- | --- | --- |
| `image` | Yes | String | Docker image reference. Must already exist on the runner. |
| `docker` | No | Boolean | Request host Docker socket access. Requires operator opt-in and matching ref-rule approval. Use only for trusted repositories and refs. |
| `env` | No | Mapping of strings | Environment keys must match `[A-Z][A-Z0-9_]*`. Keys starting with `GITMAN_` are reserved. |
| `steps` | Yes | List | At least one step, maximum 200. |
| `steps[].name` | Yes | String | Non-empty, maximum 120 characters, no CR/LF/NUL. |
| `steps[].run` | Yes | String | Non-empty shell content, no NUL. Executed by `/bin/sh` with `set -eu`. |

The file must be a regular file, not a symlink. Its maximum size is 256 KiB. Unknown YAML fields and multiple YAML documents are rejected.

## Secret references

A secret reference must be the complete environment value:

```yaml
env:
  DEPLOY_TOKEN: ${{ secrets.DEPLOY_TOKEN }}
```

Interpolation inside a larger string is not supported.

## Built-in environment variables

Every run receives:

| Variable | Meaning |
| --- | --- |
| `GITMAN_REPO` | `<owner>/<repository>` |
| `GITMAN_COMMIT` | Resolved commit hash |
| `GITMAN_BRANCH` | Branch name when applicable |
| `GITMAN_TAG` | Tag name when applicable |
| `GITMAN_EVENT` | `manual` or `push` |
| `GITMAN_RUN_ID` | CI run identifier |

User-defined environment values and stored secret values cannot contain NUL, carriage-return, or newline characters.

## Container filesystem

| Path | Mode | Purpose |
| --- | --- | --- |
| `/workspace` | Writable bind mount | Repository checkout and current working directory |
| `/gitman/artifacts` | Writable bind mount | Files copied out as artifacts |
| `/gitman/cache` | Writable bind mount when cache lock succeeds | Persistent repository-scoped cache |
| `/tmp` | Writable tmpfs, 256 MiB, executable | Temporary files and generated home directory |
| Container root filesystem | Read-only | Image contents |

The selected image must contain `/bin/sh`.

## Docker builds

A trusted repository can request access to the runner host Docker daemon:

```yaml
image: docker:29-cli
docker: true
steps:
  - name: verify Docker access
    run: docker version
```

The worker mounts `/var/run/docker.sock`, adds the socket group ID to the job container, and sets `DOCKER_HOST=unix:///var/run/docker.sock`. Operators must explicitly enable this with `GITMAN_CI_ALLOW_DOCKER_SOCKET=true`. Docker-enabled jobs effectively control the runner host Docker daemon. Do not enable this for untrusted repositories or shared multi-tenant runners.

## Network and images

The default network mode is `none`. Dependencies must come from the image, repository, or a warmed `/gitman/cache` mount. Operators can change `GITMAN_CI_NETWORK`, but doing so expands the trust boundary.

Gitman uses `docker run --pull never`. Ask an operator to pre-pull or build approved images before referencing them.

## Log format and failure handling

Gitman writes timestamped CI logs. Each shell step prints a start marker and either a success marker or a failure marker with the exit code. Failures that happen before the job container starts, such as Docker socket policy denial, missing local image, bad path mapping, or disk-limit failures, are printed with `ERROR`, `Details`, and a practical `Fix` line.

Git checkout uses detached commits internally, but Gitman's worker suppresses Git's detached-head advice so logs stay focused on CI output.

## Cache behavior

The worker serializes cache writes per repository. If it cannot obtain the cache lock promptly, the job runs without `/gitman/cache`. Keep jobs correct when the cache is absent.
