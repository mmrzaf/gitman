# CI configuration

Gitman reads `.gitman-ci.yml` from the repository root of the selected commit.

## Minimal example

```yaml
image: alpine:3.20
steps:
  - name: verify
    run: |
      echo "Repository: $GITMAN_REPO"
      echo "Commit: $GITMAN_COMMIT"
```

## Full example

```yaml
image: golang:1.24-alpine
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

## Network and images

The default network mode is `none`. Dependencies must come from the image, repository, or a warmed `/gitman/cache` mount. Operators can change `GITMAN_CI_NETWORK`, but doing so expands the trust boundary.

Gitman uses `docker run --pull never`. Ask an operator to pre-pull or build approved images before referencing them.

## Cache behavior

The worker serializes cache writes per repository. If it cannot obtain the cache lock promptly, the job runs without `/gitman/cache`. Keep jobs correct when the cache is absent.
