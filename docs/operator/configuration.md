# Configuration reference

Gitman is configured entirely through the environment; there is no
config file. `web` and `worker` read the same settings.

## Application settings (`internal/config`)

| Variable | Default | Purpose |
|---|---|---|
| `GITMAN_DATABASE_URL` | — (required) | PostgreSQL connection URL. Gitman's only data store. |
| `GITMAN_DATA_DIR` | `.data` | Root for repositories, hooks and run workspaces (`<DATA_DIR>/repos`, `/hooks`, `/workspaces`). |
| `GITMAN_PUBLIC_URL` | `http://localhost:8080` | The address people use; shown in clone URLs and push output. Must be an absolute `http`/`https` URL with no user info, query or fragment. |
| `GITMAN_WEB_URL` | the public URL | Where a worker reaches `web` to fetch a run's commit. Usually an internal address (e.g. `http://web:8080` in Compose), not the public one. |
| `GITMAN_PORT` | `8080` | The web process's listen port. |
| `GITMAN_SECRET_KEY` | empty | Encrypts repository secrets; at least 32 characters. Empty disables secret storage entirely. Changing it makes stored secrets unreadable. |
| `GITMAN_TRUSTED_PROXIES` | empty | Comma-separated IP addresses or CIDR ranges allowed to set `X-Forwarded-For` — needed for correct client-IP attribution (rate limiting, audit) behind a reverse proxy. |
| `GITMAN_RETENTION_DAYS` | `90` | Days finished runs and their logs are kept; `0` keeps them forever. Each ref's latest run and every deployment record are always kept. |
| `GITMAN_DATABASE_MAX_CONNS` | `0` (the process's own default) | Caps `web` and `worker`'s own connection pool sizes. `web` also refuses a request that cannot get a connection within a short, fixed timeout, with `503` and `Retry-After`, rather than leaving it to hang. |
| `GITMAN_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `GITMAN_LOG_FORMAT` | `text` | `text` or `json`. |

Secure cookies aren't a separate setting: the session cookie's `Secure`
flag is derived from `GITMAN_PUBLIC_URL`'s scheme (set automatically when
it's `https`). Git over HTTPS (clone, fetch, push) has a fixed
concurrency limit.

## Compose (`.env`)

`compose.yaml` hands `.env` to both services, so every application
setting above goes there. Three more are for Compose itself:

| Variable | Purpose |
|---|---|
| `GITMAN_IMAGE` | The image to run, such as `gitman:1.0.0-beta.21`. Required. |
| `GITMAN_DOMAIN` | The host name Traefik routes to Gitman. `GITMAN_PUBLIC_URL` is set from it to `https://<GITMAN_DOMAIN>`. Required. |
| `GITMAN_DATA_DIR` | Also the host path mounted into both services, at the identical path. Required. |

Compose sets `GITMAN_WEB_URL` to `http://gitman-web:8080` itself: workers
reach web directly on the `gitman_internal` network, not through Traefik.

## Image build arguments (`Dockerfile`)

Not runtime settings — passed at `docker build` time, so the image itself can be built behind a registry or module mirror:

| Build arg | Default | Purpose |
|---|---|---|
| `GO_IMAGE` | `golang:1.27-bookworm` | Builder base image. |
| `RUNTIME_IMAGE` | `debian:bookworm-slim` | Runtime base image. |
| `DOCKER_CLI_IMAGE` | `docker:29-cli` | The image the worker's `docker` client is copied from. |
| `DEBIAN_MIRROR` | `http://deb.debian.org/debian` | Debian package mirror for the runtime image. |
| `DEBIAN_SECURITY_MIRROR` | `http://security.debian.org/debian-security` | Debian security mirror for the runtime image. |
| `GOPROXY` | `https://proxy.golang.org,direct` | Go module proxy used during the build. |
| `VERSION` | `dev` | Embedded into the binary as `main.version`, shown by `gitman version`. |

## Health probes

- `GET /healthz` — process-only liveness; doesn't touch the database.
- `GET /readyz` — liveness plus a database check.
