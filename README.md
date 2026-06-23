# Gitman

Gitman is a lightweight self-hosted Git server written in Go. It stores metadata in SQLite, stores bare repositories on disk, and uses the system Git executable for Git transport operations.

Gitman is aimed at small teams and private infrastructure. It is not a multi-tenant SaaS platform and it does not attempt to replace a dedicated CI runner fleet.

## Features

- Browser UI for repositories, branches, tags, commits, files, collaborators, SSH keys, and personal access tokens.
- Git smart HTTP with personal access token authentication.
- Optional SSH Git transport through the host OpenSSH server and generated `authorized_keys` forced commands.
- Private and public repositories with read and write collaborators.
- Built-in CI jobs defined in `.gitman-ci.yml`.
- CI secrets encrypted at rest when `GITMAN_SECRET_KEY` is configured.
- CI logs, nested artifacts, repository archives, backups, and an admin CLI.

## Requirements

- Go `1.26` to build from source.
- `git` available in `PATH` at runtime.
- Docker only when the built-in CI worker is enabled.
- OpenSSH only when SSH Git transport is enabled.

The repository must include the vendored `static/` directory used by the embedded web UI.

## Build and run from source

```bash
go test ./...
go build -o bin/gitman ./cmd/gitman
mkdir -p .data
read -rsp 'Admin password: ' ADMIN_PASSWORD; printf '\n'
printf '%s\n' "$ADMIN_PASSWORD" | ./bin/gitman admin users create admin
unset ADMIN_PASSWORD
./bin/gitman web
```

The UI is available at `http://localhost:8080` by default.

Start the CI worker separately when CI is needed. Pull approved job images on the runner first: Gitman starts CI containers with `--pull never` so repository-controlled jobs cannot grow Docker storage by pulling arbitrary images.

```bash
docker pull golang:1.26-bookworm
./bin/gitman worker
```

## Docker

For the local HTTP setup:

```bash
export GIT_UID=$(id -u)
export DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
export GITMAN_DATA_DIR="$(pwd)/data"
mkdir -p "$GITMAN_DATA_DIR" && chmod 700 "$GITMAN_DATA_DIR"
docker compose up -d --build
```

Open `http://localhost:8080`. See [DOCKER_SETUP.md](DOCKER_SETUP.md) before exposing the service publicly or enabling SSH.

## CI configuration

Add `.gitman-ci.yml` at the repository root:

```yaml
image: golang:1.26-bookworm
env:
  APP_ENV: test
  GOMODCACHE: /gitman/cache/go/pkg/mod
  DEPLOY_TOKEN: ${{ secrets.DEPLOY_TOKEN }}
steps:
  - name: Dependencies
    run: |
      mkdir -p "$GOMODCACHE"
      go mod download
  - name: Test
    run: go test ./...
  - name: Save report
    run: cp coverage.out /gitman/artifacts/coverage.out
```

CI jobs run in Docker containers with network access disabled by default, a read-only root filesystem, dropped Linux capabilities, PID limits, CPU and memory limits, Docker log persistence disabled, bounded Gitman logs, bounded artifact staging, bounded workspace usage, and serialized per-repository cache writes. Manual runs can select a branch, tag, or reachable historical commit; Gitman always reads `.gitman-ci.yml` from the exact selected commit. Job images must already exist on the runner because CI uses `--pull never`. With `GITMAN_CI_NETWORK=none`, dependencies must come from the image, the repository, or an already-warmed `/gitman/cache` mount.

Non-default branches and tags do not auto-run by default and do not receive CI secrets unless the repository owner adds a matching trust rule. Rules may be exact refs or glob patterns such as `v*` for version tags. Docker socket access requires both `GITMAN_CI_ALLOW_DOCKER_SOCKET=true` and matching ref approval. Treat Docker-enabled jobs as privileged host infrastructure. Application-level disk checks limit damage but are not hard quotas. Run the worker on a dedicated runner host or inside a VM with kernel-enforced filesystem quotas before accepting untrusted repository writers.

## Admin CLI

Passwords are read from standard input so they do not appear in shell history or process listings.

```bash
read -rsp 'Password: ' USER_PASSWORD; printf '\n'
printf '%s\n' "$USER_PASSWORD" | gitman admin users create alice
unset USER_PASSWORD

read -rsp 'New password: ' USER_PASSWORD; printf '\n'
printf '%s\n' "$USER_PASSWORD" | gitman admin users reset-password alice
unset USER_PASSWORD
gitman admin users delete alice

gitman admin repos backup /srv/backups/repos-$(date +%F)
gitman admin repos backup-all /srv/backups/gitman-$(date +%F)
gitman admin repos configure-all
gitman version
```

Backup destinations must be absent or empty, and must not be inside the repository or artifact trees. Repository and artifact files are copied live. Use a maintenance window or filesystem snapshots when strict point-in-time consistency is required.

## Configuration

Core environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `GITMAN_PORT` | `8080` | HTTP listen port. |
| `GITMAN_DB` | `.data/db/gitman.sqlite` | SQLite database path. |
| `GITMAN_REPOS` | `.data/repos` | Bare repository root. |
| `GITMAN_ARTIFACTS` | `.data/artifacts` | CI log and artifact root. |
| `GITMAN_CACHE_ROOT` | `.data/ci/cache` | Persistent CI cache root. |
| `GITMAN_PUBLIC_URL` | `http://localhost:8080` | Browser-facing base URL used for clone links. |
| `GITMAN_INTERNAL_URL` | `http://localhost:8080` | URL used by generated Git hooks. |
| `GITMAN_SECRET_KEY` | empty | CI-secret encryption key. Empty disables CI-secret storage. |
| `GITMAN_ALLOW_REGISTER` | `false` | Enable public account registration. |
| `GITMAN_FORCE_SECURE_COOKIES` | `false` | Always mark browser cookies as secure. Enable behind HTTPS. |
| `GITMAN_TRUST_PROXY_HEADERS` | `false` | Trust proxy HTTPS headers. Enable only behind a trusted reverse proxy. |
| `GITMAN_GIT_RECEIVE_MAX_BYTES` | `536870912` | Git receive-pack input ceiling applied to new repos and `admin repos configure-all`. |

CI limits:

| Variable | Default |
| --- | --- |
| `GITMAN_WORKER_CONCURRENCY` | `1` |
| `GITMAN_MEMORY_LIMIT` | `512m` |
| `GITMAN_CPU_LIMIT` | `1` |
| `GITMAN_CI_TIMEOUT` | `30m` |
| `GITMAN_CI_LEASE_TIMEOUT` | `2m` |
| `GITMAN_CI_HEARTBEAT_INTERVAL` | `15s` |
| `GITMAN_CI_NETWORK` | `none` |
| `GITMAN_CI_ARTIFACT_MAX_BYTES` | `104857600` |
| `GITMAN_CI_ARTIFACT_MAX_FILES` | `1000` |
| `GITMAN_CI_LOG_MAX_BYTES` | `10485760` |
| `GITMAN_CI_WORKSPACE_ROOT` | `.data/ci/workspaces` |
| `GITMAN_CI_WORKSPACE_MAX_BYTES` | `1073741824` |
| `GITMAN_CI_CACHE_MAX_BYTES` | `1073741824` |
| `GITMAN_CI_CONTAINER_USER` | worker process numeric non-root UID:GID |
| `GITMAN_CI_ALLOW_DOCKER_SOCKET` | `false` |
| `GITMAN_CI_DOCKER_SOCKET_PATH` | `/var/run/docker.sock` |
| `GITMAN_CI_WORKER_PATH_PREFIX` | empty |
| `GITMAN_CI_HOST_PATH_PREFIX` | empty |

## Security model

- Repository writers can run repository-controlled CI code. Any CI secret exposed to a trusted ref job must be treated as accessible to writers who can modify that ref.
- CI logs and artifacts require owner or collaborator membership even when repository source code is public.
- CI logs mask exact configured secret values, but masking is defense in depth. Do not print secrets.
- CI containers are restricted, but a Docker socket is still privileged infrastructure. Pre-pull only approved images. CI jobs must run as a numeric non-root UID:GID. Enable `GITMAN_CI_ALLOW_DOCKER_SOCKET` only for trusted repositories and matching refs or patterns that require `docker: true`.
- When the worker itself runs in Docker, configure the worker and host path prefixes so sibling containers mount host-visible paths. The included Compose file does this automatically.
- Set `GITMAN_PUBLIC_URL`, force secure cookies, and trust proxy headers only when the reverse proxy is controlled and correctly configured.
