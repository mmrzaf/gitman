# Backups and upgrades

## What to back up

- **The database** — `pg_dump`. Everything except repository content
  lives here: people, tokens, repositories, ref rules, secrets, run
  history and deployments.
- **The `repos` directory** under `GITMAN_DATA_DIR` — the bare Git
  repositories themselves.

Nothing else needs backing up: Git hook scripts under `hooks/` are
regenerated at every start of `web`, and run workspaces exist only for
the duration of a run.

With the supported Compose setup, where PostgreSQL runs in its own
container named `postgres`:

```sh
docker exec postgres pg_dump -U gitman -Fc gitman > gitman.dump
sudo tar -C /srv/apps/gitman/data -czf gitman-repos.tar.gz repos
```

and to restore the database into a fresh, empty one:

```sh
docker exec -i postgres pg_restore -U gitman -d gitman < gitman.dump
```

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
there's no separate migration step to remember. Back up first, then
build the new version's image on the host, set `GITMAN_IMAGE` in `.env`
to it, and restart:

```sh
docker build --build-arg VERSION=v1.0.0-beta.22 -t gitman:1.0.0-beta.22 .
docker compose up -d
```

There is no data migration path from earlier, SQLite-based versions of
Gitman: this version's PostgreSQL schema is unrelated to that on-disk
format. Treat an upgrade from a pre-PostgreSQL install as a fresh
install.
