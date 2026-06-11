# Architecture

## Binary modes

Gitman builds one binary with four top-level commands:

| Command | Package entry | Responsibility |
| --- | --- | --- |
| `web` | `cmd/gitman/web.go` | HTTP server and browser application |
| `worker` | `cmd/gitman/worker.go` | CI polling and Docker execution |
| `serve` | `cmd/gitman/serve.go` | OpenSSH forced-command adapter |
| `admin` | `cmd/gitman/admin.go` | Account and backup administration |

## Internal packages

| Package | Responsibility |
| --- | --- |
| `internal/config` | Environment-based configuration |
| `internal/db` | SQLite initialization, migrations, and persistence |
| `internal/git` | Safe Git repository paths, Git subprocess calls, browsing, refs, and archives |
| `internal/handlers` | Router, auth, CSRF, UI, Git smart HTTP, CI triggers, logs, and artifact serving |
| `internal/ssh` | Managed `authorized_keys` generation and SSH command authorization |
| `internal/worker` | CI config validation, leases, workspaces, Docker containers, caches, logs, redaction, and artifacts |
| `internal/admin` | Username/password validation, user lifecycle, and backups |

## Embedded assets

`embed.go` embeds:

- `migrations/*.sql`
- `templates/**/*.html`
- `static/**/*`

A source-release package must retain those directories.

## Data flow: HTTP Git

1. Router resolves `<owner>/<repo>.git`.
2. Middleware checks repository visibility and HTTP Basic credentials when required.
3. Handler delegates to `git http-backend` through CGI.

## Data flow: SSH Git

1. OpenSSH reads Gitman's managed `authorized_keys` file.
2. The forced command invokes `gitman serve <keyID>` through the configured wrapper.
3. Gitman validates the original Git command, key owner, repository, and required access level.
4. Gitman starts `git-upload-pack`, `git-receive-pack`, or `git-upload-archive` for the safe bare-repository path.

## Data flow: CI

1. UI or webhook creates a pending row.
2. Worker claims the row with a new attempt ID and heartbeat lease.
3. Worker clones from the local bare repository.
4. Worker validates `.gitman-ci.yml`, resolves secrets, and creates an environment file outside the checkout.
5. Worker starts a labeled sibling Docker container with restrictions and bind mounts.
6. Worker collects regular artifacts, records final status, and removes the temporary workspace.
7. Reconciliation removes stale managed containers and requeues stale attempts after crashes.
