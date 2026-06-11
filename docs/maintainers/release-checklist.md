# Release checklist

## Validate

```bash
go test ./...
golangci-lint run
go build -trimpath -o bin/gitman ./cmd/gitman
```

Exercise at least:

- Login and logout.
- Public and private repository browse, clone, fetch, and push.
- `read` and `write` collaborator boundaries.
- Token creation, one-time display, use, and revoke.
- SSH-key add/delete and generated `authorized_keys` output when SSH is supported.
- Manual CI run, skipped run, failed run, successful run, artifact download, and auto-trigger hook.
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

`.gitignore` is not a release-packaging policy. Build archives from an allow-list or from a clean export.

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
