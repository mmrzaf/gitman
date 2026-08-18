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

Backups require the exclusive Gitman state lock, so stop the long-running processes first. With Docker Compose:

```bash
docker compose stop web worker
docker compose run --rm --no-deps web gitman admin repos backup-all /data/backups/gitman-$(date +%F)
docker compose start web worker
```

A full backup contains:

```text
<destination>/
├── db/<sqlite-file-name>
├── repos/
└── artifacts/          # when present
```

Gitman acquires an exclusive state lock before opening the database for a backup. Web, worker, SSH, and other mutating Gitman commands hold shared locks, so the backup refuses to run while any of them are active. With the exclusive lock held, SQLite, repositories, and artifacts are copied as one offline-consistent Gitman snapshot. `authorized_keys` is derived from the database and is regenerated on web startup.

The destination must be absent or empty and must not be inside the repository or artifact trees.

## What is not included

- `GITMAN_SECRET_KEY`. Preserve it separately in your secret manager.
- CI cache. It is rebuildable.
- Temporary CI workspaces.

## Restore

Gitman does not currently provide a restore command. For a standard Compose deployment:

1. Stop `web` and `worker`.
2. Create an empty replacement data directory with restrictive permissions.
3. Copy the backup `db/`, `repos/`, and `artifacts/` entries into that directory when present.
4. Restore the same externally managed `GITMAN_SECRET_KEY` value.
5. Ensure ownership matches the configured `GIT_UID`.
6. Start `web`, verify `/health`, then start `worker`.
7. Verify repository clone, push, artifact access, and SSH transport when enabled.

## Upgrade

Back up first, then rebuild:

```bash
docker compose stop web worker
docker compose run --rm --no-deps web gitman admin repos backup-all /data/backups/pre-upgrade-$(date +%F-%H%M%S)
docker compose up -d --build
```

Database migrations run forward-only during Gitman startup. Rolling back a Gitman release means restoring the matching database and filesystem backup; Gitman does not attempt reverse schema migrations.
