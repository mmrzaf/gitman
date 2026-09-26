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
it's `https`). Git HTTP (clone, fetch, push) has a fixed, not
independently tunable, concurrency limit; there are no other
independently tunable Smart-HTTP, file-search or repository-browse
limits present in earlier versions of Gitman.

## Compose-level settings (`.env`)

These aren't read by `gitman` itself; they're substituted into
`compose.yaml`.

| Variable | Default | Purpose |
|---|---|---|
| `GITMAN_HOST` | — (required) | The hostname Traefik routes to Gitman; also used to build `GITMAN_PUBLIC_URL`. |
| `POSTGRES_PASSWORD` | — (required) | PostgreSQL's password; part of the generated `GITMAN_DATABASE_URL`. |
| `GITMAN_DATA_DIR` | `/srv/gitman` | Host path bind-mounted into `web` and `worker` at the identical path. |
| `GITMAN_SECRET_KEY` | empty | Passed through to the application setting above. |
| `GITMAN_TRUSTED_PROXIES` | `172.16.0.0/12` | Passed through to the application setting above; the default covers Docker's default bridge networks. |
| `GITMAN_RETENTION_DAYS` | `90` | Passed through to the application setting above. |
| `GITMAN_DATABASE_MAX_CONNS` | `0` | Passed through to the application setting above. |
| `GITMAN_LOG_LEVEL` | `info` | Passed through to the application setting above. |
| `GITMAN_LOG_FORMAT` | `text` | Passed through to the application setting above. |
| `TRAEFIK_NETWORK` | `traefik` | The external Docker network Traefik and `web` share. |
| `TRAEFIK_ENTRYPOINT` | `websecure` | Traefik entry point for Gitman's router. |
| `TRAEFIK_CERTRESOLVER` | `letsencrypt` | Traefik certificate resolver for Gitman's router. |
| `POSTGRES_IMAGE` | `postgres:16-alpine` | Image tag, to use a registry mirror. |
| `GITMAN_IMAGE` | `gitman:latest` | Image tag, to use a registry mirror. |

## Image build arguments (`Dockerfile`)

Not runtime settings — passed at `docker build`/`docker compose build`
time, so the image itself can be built behind a registry or module mirror:

| Build arg | Default | Purpose |
|---|---|---|
| `GO_IMAGE` | `golang:1.27-alpine` | Builder base image. |
| `RUNTIME_IMAGE` | `alpine:3.20` | Runtime base image. |
| `GOPROXY` | `https://proxy.golang.org,direct` | Go module proxy used during the build. |
| `ALPINE_MIRROR` | empty (public default) | Alpine package mirror, if set. |
| `VERSION` | `dev` | Embedded into the binary as `main.version`, shown by `gitman version`. |

## Health probes

- `GET /healthz` — process-only liveness; doesn't touch the database.
- `GET /readyz` — liveness plus a database check.

## Settings with no equivalent in this version

The rewrite is deliberately smaller in scope than earlier Gitman
versions. Settings that controlled SQLite paths, CI artifact/cache
storage, SSH, self-registration, independently tunable per-endpoint
concurrency limits, worker resource limits (memory/CPU), and CI
storage/heartbeat tuning have no counterpart: those features (SSH,
self-registration, CI artifacts and caches) don't exist in this version
at all. See [Not added, on purpose](../../README.md#not-added-on-purpose).

Per-repository read access is back, unlike in the version immediately
before this rewrite: see [Repository read
access](security.md#repository-read-access).

Docker access for pipelines moved from an instance-wide setting to a
per-ref grant: see `--docker` in
[`gitman admin rule set`](../reference/cli.md) and
[Security model](security.md).
