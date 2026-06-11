# Development guide

## Prerequisites

- Go `1.24.6`.
- `git` in `PATH`.
- Docker only for exercising the CI worker.

## Common commands

```bash
go test ./...
go build -o bin/gitman ./cmd/gitman
gofmt -w .
golangci-lint run
```

The repository also includes `Makefile` and `Justfile` shortcuts. Prefer the explicit commands above when validating changes.

## Local web process

```bash
mkdir -p .data
printf '%s\n' 'replace-with-a-strong-password' | ./bin/gitman admin users create admin
GITMAN_LOG_LEVEL=debug ./bin/gitman web
```

## Local worker

```bash
docker pull alpine:3.20
GITMAN_LOG_LEVEL=debug ./bin/gitman worker
```

Run workers only on development hosts where Docker execution is acceptable.

## Areas requiring regression coverage

- Path containment and symlink rejection.
- Session, token, and collaborator access checks.
- Public source versus member-only CI output.
- CSRF enforcement for browser mutations.
- SSH forced-command parsing and generated `authorized_keys` output.
- CI config strict parsing.
- Secret redaction across chunk boundaries.
- Attempt lease reclamation after worker crashes.
- Artifact file-count, byte, symlink, and traversal handling.
- Backup destination containment and snapshot layout.

## Packaging

Use [the release checklist](maintainers/release-checklist.md). Do not rely on `.gitignore` to keep runtime state out of archives.
