# Release checklist

## Validate

```bash
VERSION=vX.Y.Z make verify
VERSION=vX.Y.Z make release-source
```

`make verify` is intentionally network-independent and does not run vulnerability scanning. Before release, require the GitHub CI/release `govulncheck` job to pass on the exact release commit with the pinned Go toolchain.

Gitman server binaries are released for Linux amd64 and arm64. Do not add another server OS to the release matrix until Git transport hooks, CI, filesystem semantics, and the full integration suite are supported there.

Exercise at least:

- Login and logout.
- Public and private repository browse, clone, fetch, and push.
- `read` and `write` collaborator boundaries.
- Token creation, one-time display, read/write scope enforcement, finite expiration, last-used update, expired-token rejection, and revoke.
- SSH-key add/delete and generated `authorized_keys` output when SSH is supported, including a configured Gitman binary path containing spaces or shell quotes.
- Manual CI run for the default branch, non-default branch, tag, reachable historical commit, skipped run, failed run, successful run, artifact download/preview, trusted-ref rules, and automatic push-trigger delivery.
- CI cancellation for both a pending and running job, retry lineage, structured/live incremental logs, UTF-8 log boundaries, search/follow/wrap/raw views, and repository deletion refusal until a cancelled worker has stopped.
- Worker heartbeat lifecycle, Docker-unavailable claim pause/recovery, exact-attempt requeue, low-free-space/inode admission pause, and workspace/cache/artifact entry-count enforcement.
- Durable push triggers while the web process is stopped, including an annotated tag followed by ref deletion before restart.
- Repository description and visibility updates, owner-only settings access, and quarantined deletion cleanup.
- Repository home, README preview, commit/diff pages, exact-revision source links, line/range permalinks, Go to File, CI-run navigation back to its branch/tag context, and narrow-screen navigation.
- Root/merge/rename/delete/binary commit inspection plus odd Unicode/whitespace filenames and source-rendering limits.
- `/healthz` liveness, `/readyz` web readiness, and `/ci-healthz` worker-fleet readiness behavior without session cookies or worker-detail leakage.
- Git HTTP clone and push with a personal access token, and rejection with the account password.
- Git HTTP large-push smoke: push 3–10 MiB incompressible data with stock Git defaults, then clone/fetch and verify the resulting commit.
- A push larger than `GITMAN_GIT_RECEIVE_MAX_BYTES` is rejected cleanly without corrupting the repository.
- Public archive/raw/download streams honor global/per-client concurrency limits and terminate at `GITMAN_REPO_STREAM_TIMEOUT`.
- Pathological browse fixtures (very large directory, very large commit, and thousands of refs) render bounded/truncation-aware pages without unbounded memory growth.
- Password validation rejects inputs over bcrypt's 72-byte limit; password reset revokes existing sessions and tokens and records a non-secret audit event.
- Login success/failure, self-registration, and security-sensitive admin, token, SSH-key, repository, collaborator, CI secret, and CI trusted-ref mutations record audit events without credential/secret values.
- Beta-17 database upgrade preserves existing tokens while assigning their 90-day rotation deadline, and creates the audit trail schema.
- Unauthorized repository-settings POSTs return 403 without rendering collaborator, CI-secret-name, or trusted-ref data.
- Backup creation and restore drill.

## Package only source inputs

Do not package runtime state or development metadata. Explicitly exclude:

```text
.git/
.data/
data/
*.sqlite
bin/
coverage.*
*.out
.env
```

Also check for generated `authorized_keys`, CI logs, CI artifacts, repositories, tokens, secrets, and local credentials.

`.gitignore` is not a release-packaging policy. Build source archives from tracked files with `scripts/release-source-archive.sh`.

## Verify archive contents

```bash
tar -tzf <archive>.tar.gz | sort
```

The archive must retain embedded inputs:

```text
migrations/
templates/
static/
```

## Deployment notes

Document:

- Database migration impact.
- Required Go and Docker versions when changed.
- New environment variables and defaults.
- Backup and rollback steps.
- CI behavior or security-boundary changes.

## Version output

Verify release injection before publishing:

```bash
go build -trimpath -ldflags "-X main.version=vX.Y.Z" -o bin/gitman ./cmd/gitman
bin/gitman version
bin/gitman --version
```

