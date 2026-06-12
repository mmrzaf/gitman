# Release checklist

## Validate

```bash
go test ./...
go vet ./...
golangci-lint run
govulncheck ./...
go build -trimpath -o bin/gitman ./cmd/gitman
docker build -t gitman:verify .
VERSION=v1.0.0-beta.11 make release-source
```

Exercise at least:

- Login and logout.
- Public and private repository browse, clone, fetch, and push.
- `read` and `write` collaborator boundaries.
- Token creation, one-time display, use, and revoke.
- SSH-key add/delete and generated `authorized_keys` output when SSH is supported.
- Manual CI run for the default branch, non-default branch, tag, skipped run, failed run, successful run, artifact download, trusted-ref rules, and auto-trigger hook.
- Git HTTP clone and push with a personal access token, and rejection with the account password.
- Password reset revokes existing sessions and tokens.
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
go build -trimpath -ldflags "-X main.version=v1.0.0-beta.11" -o bin/gitman ./cmd/gitman
bin/gitman version
bin/gitman --version
```
