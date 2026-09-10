# Configuration reference

Gitman is configured with environment variables.

## Web and shared settings

| Variable | Default | Purpose |
| --- | --- | --- |
| `GITMAN_PORT` | `8080` | Web listen port. |
| `GITMAN_DB` | `.data/db/gitman.sqlite` | SQLite database path. |
| `GITMAN_REPOS` | `.data/repos` | Bare repository root. |
| `GITMAN_ARTIFACTS` | `.data/artifacts` | CI log and artifact root. |
| `GITMAN_CACHE_ROOT` | `.data/ci/cache` | Persistent CI cache root. |
| `GITMAN_AUTH_KEYS` | `.data/authorized_keys` | Generated SSH `authorized_keys` file. |
| `GITMAN_BINARY_PATH` | Current executable absolute path | Command written into generated SSH forced commands. Use a host wrapper for Docker deployments with SSH. |
| `GITMAN_SSH_USER` | `git` | SSH username displayed in clone links. |
| `GITMAN_SERVER_HOST` | `localhost` | Hostname displayed in SSH clone links. It does not control the HTTP bind address. |
| `GITMAN_PUBLIC_URL` | `http://<server-host>:<port>` | Browser-facing base URL used in HTTP clone links. Trailing slash is removed. |
| `GITMAN_SECRET_KEY` | Empty | Passphrase for encrypting repository CI secrets. Empty disables CI-secret storage. |
| `GITMAN_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. Invalid explicit values fail startup. |
| `GITMAN_ALLOW_REGISTER` | `false` | Enables public account registration. |
| `GITMAN_FORCE_SECURE_COOKIES` | `false` | Always marks browser cookies secure. Enable behind HTTPS. |
| `GITMAN_TRUST_PROXY_HEADERS` | `false` | Trusts proxy HTTPS headers. Enable only behind a trusted reverse proxy. |
| `GITMAN_GIT_RECEIVE_MAX_BYTES` | `536870912` | Configures `receive.maxInputSize` for new bare repositories and `admin repos configure-all`. Invalid or non-positive values fail startup. |
| `GITMAN_GIT_HTTP_MAX_CONCURRENT` | `16` | Maximum concurrent `git-http-backend` processes per web instance. Excess requests receive `503` with `Retry-After`. |
| `GITMAN_GIT_HTTP_MAX_CONCURRENT_PER_IP` | `4` | Maximum concurrent Smart HTTP operations from one resolved client IP. |
| `GITMAN_GIT_HTTP_TIMEOUT` | `30m` | Hard lifetime for one Smart HTTP Git backend process. Client disconnects and timeout cancellation terminate the child process. |
| `GITMAN_FILE_SEARCH_MAX_CONCURRENT` | `8` | Maximum simultaneous repository file-search subprocesses per web instance. Excess searches receive `503` with `Retry-After`. |
| `GITMAN_FILE_SEARCH_MAX_CONCURRENT_PER_IP` | `2` | Maximum simultaneous file searches from one resolved client IP. |
| `GITMAN_FILE_SEARCH_MAX_FILES` | `100000` | Maximum blob paths examined by one repository file-search request. |
| `GITMAN_FILE_SEARCH_MAX_BYTES` | `33554432` | Maximum NUL-delimited tree output consumed by one repository file-search request. |
| `GITMAN_FILE_SEARCH_TIMEOUT` | `5s` | Maximum repository tree-enumeration time for one file-search request. Limited searches return partial results marked as truncated. |
| `GITMAN_REPO_BROWSE_MAX_CONCURRENT` | `16` | Maximum concurrent repository tree/blob/commit browser requests doing Git inspection work. |
| `GITMAN_REPO_BROWSE_MAX_CONCURRENT_PER_IP` | `4` | Maximum concurrent repository browser requests attributed to one client IP. |
| `GITMAN_REPO_BROWSE_TIMEOUT` | `10s` | Maximum Git-inspection lifetime for one browser repository request. |
| `GITMAN_REPO_STREAM_MAX_CONCURRENT` | `8` | Maximum concurrent raw blob, download, and source-archive streams. |
| `GITMAN_REPO_STREAM_MAX_CONCURRENT_PER_IP` | `2` | Maximum concurrent repository streams attributed to one client IP. |
| `GITMAN_REPO_STREAM_TIMEOUT` | `15m` | Maximum lifetime of one raw/download/archive stream. |

## Public Git and repository browsing limits

Git Smart HTTP uses global and per-client-IP process limits plus an operation deadline. These limits apply per web process; a reverse proxy should still enforce connection-level limits for internet-facing deployments. Behind a reverse proxy, set `GITMAN_TRUST_PROXY_HEADERS=true` only when clients cannot bypass that proxy; otherwise every proxied client intentionally shares the proxy address for per-IP limits. Gitman does not forward HTTP authorization or cookie headers into `git-http-backend`.

Go-to-file search streams `git ls-tree` output and retains only the best 40 matches in memory. Enumeration stops at the configured file-count, byte, or time limit and returns the best partial result set with a truncation marker so the browser can ask the user to narrow the query.

Repository browser pages use a separate short-lived concurrency/deadline boundary. Branch/tag selectors, directory listings, commit ref lists, and commit diff metadata are presentation-capped so hostile repository contents cannot force unbounded response memory. The UI explicitly marks capped lists/statistics instead of presenting them as complete. Raw blobs, downloads, and source archives use a longer independent stream pool with per-IP/global concurrency limits and a hard response lifetime.

## Worker settings

| Variable | Default | Purpose |
| --- | --- | --- |
| `GITMAN_WORKER_CONCURRENCY` | `1` | Number of worker polling goroutines. |
| `GITMAN_MEMORY_LIMIT` | `512m` | Docker memory limit for job containers. |
| `GITMAN_CPU_LIMIT` | `1` | Docker CPU limit for job containers. |
| `GITMAN_CI_TIMEOUT` | `30m` | Maximum duration of one run. Durations such as `30m` or positive seconds are accepted. |
| `GITMAN_CI_LEASE_TIMEOUT` | `2m` | Stale-attempt lease timeout. |
| `GITMAN_CI_HEARTBEAT_INTERVAL` | `15s` | Attempt heartbeat cadence. Must be no more than one third of the lease timeout. |
| `GITMAN_CI_NETWORK` | `none` | Docker network mode for jobs. |
| `GITMAN_CI_ARTIFACT_MAX_BYTES` | `104857600` | Maximum artifact staging bytes per run. |
| `GITMAN_CI_ARTIFACT_MAX_FILES` | `1000` | Maximum regular artifact files published per run. |
| `GITMAN_CI_ARTIFACT_MAX_ENTRIES` | `5000` | Maximum live artifact-staging filesystem entries (files, directories, and symlinks) per run. |
| `GITMAN_CI_LOG_MAX_BYTES` | `10485760` | Maximum stored CI log bytes per run. |
| `GITMAN_CI_WORKSPACE_ROOT` | `.data/ci/workspaces` | Temporary worker workspace root. |
| `GITMAN_CI_WORKSPACE_MAX_BYTES` | `1073741824` | Maximum workspace bytes checked while clone/jobs run. |
| `GITMAN_CI_WORKSPACE_MAX_ENTRIES` | `200000` | Maximum workspace filesystem entries checked while clone/jobs run. |
| `GITMAN_CI_CACHE_MAX_BYTES` | `1073741824` | Maximum repository cache bytes checked by the worker. |
| `GITMAN_CI_CACHE_MAX_ENTRIES` | `100000` | Maximum repository cache filesystem entries checked by the worker. |
| `GITMAN_CI_STORAGE_MIN_FREE_BYTES` | `1073741824` | Minimum free bytes required on worker storage before new CI work is admitted. |
| `GITMAN_CI_STORAGE_MIN_FREE_INODES` | `10000` | Minimum free filesystem inodes required before new CI work is admitted. |
| `GITMAN_CI_CONTAINER_USER` | Worker process numeric UID:GID | Numeric non-root UID:GID used inside jobs. Root is rejected. |
| `GITMAN_CI_ALLOW_DOCKER_SOCKET` | `false` | Allow pipelines with `docker: true` to receive the host Docker socket. Enable only on trusted dedicated runners. |
| `GITMAN_CI_DOCKER_SOCKET_PATH` | `/var/run/docker.sock` | Worker-visible Docker socket path passed through to Docker-enabled jobs. |
| `GITMAN_CI_WORKER_PATH_PREFIX` | Empty | Worker-visible prefix translated for sibling-container bind mounts. Set with host prefix. |
| `GITMAN_CI_HOST_PATH_PREFIX` | Empty | Docker-host-visible prefix translated for sibling-container bind mounts. Set with worker prefix. |

The worker records a durable process heartbeat in SQLite. New CI claims pause automatically while Docker, SQLite, or configured worker storage is unhealthy; Docker-daemon outages are requeued instead of turning a transient runner outage into a failed build. Storage reserve checks also run during clone/container execution, and entry-count limits protect against inode exhaustion in addition to byte limits.

Explicitly configured booleans, positive integers, byte limits, and durations are validated before startup. Invalid values fail with the exact environment-variable name instead of silently changing behavior. Gitman also validates URLs, paths, log level, and the relationship between heartbeat and lease durations.


## Health probes

- `GET /healthz` is a process-only liveness probe and does not query SQLite.
- `GET /readyz` checks SQLite plus the configured repository and artifact directories, including a non-destructive writability check.
- `GET /health` is the backward-compatible readiness endpoint used by the included Compose file.
- `GET /ci-healthz` reports whether at least one recently heartbeating CI worker is currently healthy. CI is optional, so this probe is intentionally separate from web readiness. The response contains aggregate worker/job counts only; it does not expose hostnames, filesystem paths, or worker error text.

Probe routes bypass browser session and CSRF middleware, return JSON with `Cache-Control: no-store`, and do not set cookies.

## Repository receive limits

New repositories receive the configured Git `receive.maxInputSize` limit. After changing `GITMAN_GIT_RECEIVE_MAX_BYTES` or upgrading existing repositories, reconcile managed repositories with:

```bash
gitman admin repos configure-all
```

The command walks repository records, keeps paths contained under `GITMAN_REPOS`, refuses missing or symlinked repository paths, applies the receive-pack limit, and reconciles Gitman's managed CI post-receive hook without overwriting an operator-owned hook.

## Docker Compose naming adapter

The included Compose file accepts host variables `GITMAN_CI_MEMORY_LIMIT` and `GITMAN_CI_CPU_LIMIT`, then passes them to the worker as `GITMAN_MEMORY_LIMIT` and `GITMAN_CPU_LIMIT`. Direct worker deployments must use the worker variable names.

## Production configuration warnings

The `web` and `worker` commands emit explicit startup warnings for configurations that are valid but risky or incomplete in production. `gitman admin status` reports the same warnings without starting a service. Current checks include:

- HTTPS public URLs where Gitman cannot reliably mark proxy-terminated sessions `Secure`.
- Plain HTTP on non-loopback hosts.
- Forced secure cookies paired with an HTTP public URL.
- Public self-registration.
- Missing `GITMAN_SECRET_KEY` (CI secret storage unavailable).
- CI Docker-socket access.

Warnings do not replace configuration validation: malformed values still fail startup.
