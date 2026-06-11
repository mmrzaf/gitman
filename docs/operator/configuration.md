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
| `GITMAN_INTERNAL_URL` | `http://localhost:8080` | URL embedded into generated CI post-receive hooks. Trailing slash is removed. |
| `GITMAN_SECRET_KEY` | Empty | Passphrase for encrypting repository CI secrets. Empty disables CI-secret storage. |
| `GITMAN_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. Unknown values fall back to `info`. |
| `GITMAN_ALLOW_REGISTER` | `false` | Enables public account registration. |
| `GITMAN_FORCE_SECURE_COOKIES` | `false` | Always marks browser cookies secure. Enable behind HTTPS. |
| `GITMAN_TRUST_PROXY_HEADERS` | `false` | Trusts proxy HTTPS headers. Enable only behind a trusted reverse proxy. |

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
| `GITMAN_CI_ARTIFACT_MAX_FILES` | `1000` | Maximum collected artifact files per run. |
| `GITMAN_CI_LOG_MAX_BYTES` | `10485760` | Maximum stored CI log bytes per run. |
| `GITMAN_CI_WORKSPACE_ROOT` | `.data/ci/workspaces` | Temporary worker workspace root. |
| `GITMAN_CI_WORKSPACE_MAX_BYTES` | `1073741824` | Maximum workspace usage checked by the worker. |
| `GITMAN_CI_CACHE_MAX_BYTES` | `1073741824` | Maximum repository cache usage checked by the worker. |
| `GITMAN_CI_CONTAINER_USER` | Worker process numeric UID:GID | Numeric non-root UID:GID used inside jobs. Root is rejected. |
| `GITMAN_CI_WORKER_PATH_PREFIX` | Empty | Worker-visible prefix translated for sibling-container bind mounts. Set with host prefix. |
| `GITMAN_CI_HOST_PATH_PREFIX` | Empty | Docker-host-visible prefix translated for sibling-container bind mounts. Set with worker prefix. |

Invalid positive integer or duration values silently fall back to defaults during config loading. Worker startup performs additional validation for critical values.

## Docker Compose naming adapter

The included Compose file accepts host variables `GITMAN_CI_MEMORY_LIMIT` and `GITMAN_CI_CPU_LIMIT`, then passes them to the worker as `GITMAN_MEMORY_LIMIT` and `GITMAN_CPU_LIMIT`. Direct worker deployments must use the worker variable names.
