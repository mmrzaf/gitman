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

## Beta 17 to beta 18 migration

Beta 18 applies migrations 008 through 012. They add personal-access-token expiration/last-use metadata and scopes, the durable audit trail, CI worker heartbeat state, and indexes for audit and CI queue queries.

Existing beta-17 personal access tokens are preserved, keep write compatibility (`repo:write`), and receive an expiration approximately 90 days after the first beta-18 migration. Rotate long-lived automation credentials during that window. New tokens default to read-only and must have a finite lifetime.

Before first starting beta 18:

1. Stop the web and worker processes.
2. Take a full backup and preserve the current `GITMAN_SECRET_KEY` separately.
3. Start beta 18 and wait for migrations to finish.
4. Run `gitman admin status` and verify schema 12, writable storage, and CI worker health when CI is in use.
5. Verify login, clone/fetch, a write-scoped push, SSH when enabled, and one CI run.

Restoring a beta-17 binary requires restoring the matching pre-upgrade database and filesystem backup; migrations are forward-only.
