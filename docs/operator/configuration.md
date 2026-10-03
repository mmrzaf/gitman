# Configuration reference

Gitman is configured entirely through the environment; there is no
config file. `web` and `worker` read the same settings.

## Application settings (`internal/config`)

| Variable | Default | Purpose |
|---|---|---|
| `GITMAN_DATABASE_URL` | — (required) | PostgreSQL connection URL (`postgres://` or `postgresql://`). Keyword-form libpq/pgx connection strings are not accepted. Gitman's only data store. |
| `GITMAN_DATA_DIR` | `.data` | Root for repositories, hooks and run workspaces (`<DATA_DIR>/repos`, `/hooks`, `/workspaces`). |
| `GITMAN_PUBLIC_URL` | `http://localhost:8080` | The address people use; shown in clone URLs and push output. Must be an absolute `http`/`https` URL at the origin root, with no path prefix, user info, query or fragment. |
| `GITMAN_WEB_URL` | the public URL | Where a worker reaches `web` to fetch a run's commit. Usually an internal address (e.g. `http://web:8080` in Compose), not the public one. |
| `GITMAN_PORT` | `8080` | The web process's listen port. |
| `GITMAN_SECRET_KEY` | empty | Encrypts repository secrets; base64 encoding of exactly 32 random bytes. Empty disables secret storage entirely. Changing it makes stored secrets unreadable. |
| `GITMAN_TRUSTED_PROXIES` | empty | Comma-separated IP addresses or CIDR ranges allowed to set `X-Forwarded-For` — needed for correct client-IP attribution (rate limiting, audit) behind a reverse proxy. |
| `GITMAN_LOG_RETENTION_DAYS` | `30` | Log retention, independent from summaries. |
| `GITMAN_RUN_RETENTION_DAYS` | `90` | Run metadata; preserves current-head summaries. |
| `GITMAN_AUDIT_RETENTION_DAYS` | `365` | Push, refusal, event and completed-operation retention. |
| `GITMAN_DEPLOYMENT_RETENTION_DAYS` | `365` | Deployment history; preserves latest per target. |
| `GITMAN_DATABASE_MAX_CONNS` | `0` (the process's own default) | Must be 0 or 4–1000. Caps `web` and `worker`'s own connection pool sizes. `web` also refuses a request that cannot get a connection within a short, fixed timeout, with `503` and `Retry-After`, rather than leaving it to hang. |
| `GITMAN_STEP_MEMORY_MIB` | `2048` | Memory including swap per step container, in MiB. |
| `GITMAN_STEP_CPUS` | `2` | CPU quota per step container. |
| `GITMAN_STEP_PIDS` | `256` | Maximum processes per step container. |
| `GITMAN_WORKSPACE_GIB` | `10` | Monitored workspace size per run, in GiB. |
| `GITMAN_DISK_RESERVE_GIB` | `5` | Minimum free space for pushes and worker execution, in GiB. |
| `GITMAN_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `GITMAN_LOG_FORMAT` | `text` | `text` or `json`. |

`GITMAN_DATABASE_URL` is the database connection contract passed to every
Gitman process, including Git hook subprocesses. Put every connection
parameter those subprocesses need in the URL itself; hooks deliberately do not
inherit ambient `PG*` variables such as `PGPASSWORD`.

Secure cookies aren't a separate setting: the session cookie's `Secure`
flag is derived from `GITMAN_PUBLIC_URL`'s scheme (set automatically when
it's `https`). Git over HTTPS (clone, fetch, push) has a fixed
concurrency limit.

## Compose (`.env`)

`.env` supplies app settings to web and workers. Compose additionally uses:

| Variable | Default | Purpose |
|---|---|---|
| `GITMAN_IMAGE` | `gitman:local` | App image built from the repository. |
| `GITMAN_DATABASE_PASSWORD` | required | URL-safe password for the included PostgreSQL. |
| `GITMAN_DATABASE_PORT` | `5433` | Host database port, published on loopback. |
| `GITMAN_DATA_DIR` | required | Absolute host directory, mounted at the same path. |
| `GITMAN_BIND_ADDRESS` | `127.0.0.1` | Host interface for the app's HTTP port. |
| `GITMAN_HTTP_PORT` | `8080` | Host HTTP port. |
| `GITMAN_DOMAIN` | required for production | Public domain for the HTTPS example. |

Compose generates the database URL for its `db` service and sets
`GITMAN_WEB_URL=http://web:8080`. It initializes the data directory and waits
for PostgreSQL and web readiness before workers start. No fixed container
names or external networks are required.

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

Retention settings accept 1–36500 days. Zero does not disable retention.
The `.gitman-instance` marker in repository storage pairs it with the database.
To move storage, stop services, copy the complete data directory (including the
marker), change `GITMAN_DATA_DIR` and restart. Workers and their Docker host must
still agree on workspace mount paths. A separate pool of up to four connections
per process handles maintenance admission without consuming query connections.
