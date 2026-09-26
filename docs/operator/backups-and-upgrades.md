# Backups and upgrades

## What to back up

- **The database** — `pg_dump`. Everything except repository content
  lives here: people, tokens, repositories, ref rules, secrets, run
  history and deployments.
- **The `repos` directory** under `GITMAN_DATA_DIR` — the bare Git
  repositories themselves.

Nothing else needs backing up: Git hook scripts under `hooks/` are
regenerated at every start of `web` or `worker`, and run workspaces exist
only for the duration of a run.

## Restoring

If you restore the database and `repos/` from different points in time,
run `gitman admin repo sync <name>` for each affected repository
afterward, to rebuild its ref index from what's actually on disk.

## Retention

The web process prunes expired sessions and finished runs older than
`GITMAN_RETENTION_DAYS` (default 90; `0` keeps everything) once an hour.
Each ref's latest run and every deployment record are kept regardless of
age.

## Upgrades

Database migrations run automatically whenever `web` or `worker` starts —
there's no separate migration step to remember. To pin a version, set
`GITMAN_IMAGE` in `.env` to a specific tag rather than `latest`.

There is no data migration path from earlier, SQLite-based versions of
Gitman: this version's PostgreSQL schema is unrelated to that on-disk
format. Treat an upgrade from a pre-PostgreSQL install as a fresh
install.
