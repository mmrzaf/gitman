# Backups, restore, and upgrades

## Repository-only backup

```bash
gitman admin repos backup /srv/backups/repos-$(date +%F)
```

This copies the repository tree only.

## Full backup

```bash
gitman admin repos backup-all /srv/backups/gitman-$(date +%F)
```

With Docker Compose:

```bash
docker compose exec -T web gitman admin repos backup-all /data/backups/gitman-$(date +%F)
```

A full backup contains:

```text
<destination>/
├── db/<sqlite-file-name>
├── repos/
├── artifacts/          # when present
└── authorized_keys     # when present
```

The database is copied coherently with SQLite `VACUUM INTO`. Repository and artifact files are copied live. Use a maintenance window or filesystem snapshot for strict point-in-time consistency.

The destination must be absent or empty and must not be inside the repository or artifact trees.

## What is not included

- `GITMAN_SECRET_KEY`. Preserve it separately in your secret manager.
- CI cache. It is rebuildable.
- Temporary CI workspaces.

## Restore

Gitman does not currently provide a restore command. For a standard Compose deployment:

1. Stop `web` and `worker`.
2. Create an empty replacement data directory with restrictive permissions.
3. Copy the backup `db/`, `repos/`, `artifacts/`, and `authorized_keys` entries into that directory when present.
4. Restore the same externally managed `GITMAN_SECRET_KEY` value.
5. Ensure ownership matches the configured `GIT_UID`.
6. Start `web`, verify `/health`, then start `worker`.
7. Verify repository clone, push, artifact access, and SSH transport when enabled.

## Upgrade

Back up first, then rebuild:

```bash
docker compose exec -T web gitman admin repos backup-all /data/backups/pre-upgrade-$(date +%F-%H%M%S)
docker compose up -d --build
```

Database migrations run during Gitman startup.
