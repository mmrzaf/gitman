# Backups, recovery and upgrades

A backup is one maintenance snapshot of PostgreSQL and the bare Git repositories.
Keep the encryption key separately. A database dump and repository archive taken
independently are not a recoverable Gitman backup.

## Create a snapshot

Run these commands with the installation's environment and service account:

```sh
gitman admin maintenance enable
gitman admin maintenance status
gitman admin maintenance backup /protected-backups/gitman-2026-10-01
# Resume only after the snapshot completes:
gitman admin maintenance disable
```

Maintenance stops new mutations and claims while workers finish current runs.
`status` refuses backup readiness until running executions, unremoved containers,
deployment owners and repository operations are resolved. Check the **Workers**
and **Operations** pages, or `admin operation list`. Image operations are
host-targeted and wait for maintenance to end; they do not block backup
readiness because no worker may start one while maintenance is enabled.
Recover pending repository operations with `admin operation recover`; if
recovery keeps failing and the operation has no Git receipt, `admin operation
abandon --reason TEXT <id>` releases the repository after you have inspected
the half-finished state. For a worker whose host is gone for good, run
`admin worker release --confirm <worker-id>` **only after confirming its
containers are stopped**. For a killed worker on a host that still exists, use
`admin worker cleanup` there with its original workspace path. Never release
target ownership by deleting database rows.

`backup` holds an exclusive admission lease, so another process cannot resume
writes during the snapshot. It requires a new destination outside the data
directory and writes `database.dump`, `repos.tar.gz` and a final checksummed
`manifest.json`. Failed attempts do not have a valid final manifest. PostgreSQL's
`pg_dump` and `pg_restore` must be installed where these commands run, with a
client version matching or newer than the database server. The Gitman image does
not include PostgreSQL tools, and the database is a separate service: run backup
and restore with the native Gitman binary on a host that has the clients and can
reach both the database and the repository data. A PostgreSQL 18 client is
required for the Compose database; a Debian bookworm `postgresql-client`
(v15) is too old. To take a dump from a machine with only Docker, run
`pg_dump` in a PostgreSQL 18 container against the compose network, but keep the
repository archive and manifest step on the host that runs Gitman, or install the
matching client there.

Keep `GITMAN_SECRET_KEY` in a separate protected store. The manifest records only
its SHA-256 fingerprint. Losing the key loses the encrypted repository secrets.
The snapshot contains credential hashes, secret ciphertext, source and logs;
protect it as production data. Copy it to independent storage and regularly test
restoration.


## Restore

Stop the former installation. Prepare a new **empty database** and **empty data
directory**, with the same secret key and Gitman version as the backup:

```sh
export GITMAN_DATABASE_URL='postgres://gitman:password@localhost/gitman_restore'
export GITMAN_DATA_DIR=/srv/gitman-restored
# Supply the separately recovered GITMAN_SECRET_KEY.
gitman restore /protected-backups/gitman-2026-10-01
gitman web
```

Restore refuses populated destinations, damaged checksums and a different key.
It verifies the schema, instance identity, Git object integrity and secret
decryption. The archived storage marker verifies the database pairing at the
new path. Restore leaves
maintenance enabled. Verify sign-in, permissions, clones, retained run commits,
logs and deployment records before `admin maintenance disable`. Workspaces and
hooks are recreated; scripts are never rerun as part of recovery.

## Retention and budgets

Log, run, audit and deployment retention are independent (30, 90, 365 and 365 days
by default). Only the latest run summary for an indexed **current commit** and
the latest deployment of each target survive their age limits. Logs expire even
when a summary survives. Deleted refs do not keep their last run forever.

Storage is capped at 16 MiB per step and 1 GiB of retained logs per repository.
Incomplete output is marked in the UI. Reaching the retained-log cap truncates
additional output; it does not prevent execution. Run admission is paused at 10,000 pending
runs per repository. Retained Git objects are pinned until their database records
expire; collection reconciles pins after database retention, and
`gitman admin repo gc` (or Settings → Maintenance) repacks a repository
without removing anything those pins retain. It takes the repository's
mutation lock, so pushes wait rather than fail. Monitor disk space:
the configured free reserve pauses pushes and claims (5 GiB by default), and
active executions enforce a configured workspace budget (10 GiB by default). These are monitored limits, not filesystem quotas.

## Upgrading in place

Back up first — a failed upgrade is recovered by restoring that snapshot, not by
downgrading the database.

1. Enable maintenance and finish or resolve everything `status` lists, then take
   a backup as above. Keep `GITMAN_SECRET_KEY` separately.
2. Replace the binary or rebuild the image. For the Compose setup:

   ```sh
   docker compose build --pull
   docker compose up -d
   ```

   `up` recreates only the containers whose image or configuration changed. Web
   and worker run pending migrations on start; a binary refuses to start if the
   database schema is newer than the migrations it embeds, rather than serving
   against a schema it does not understand.
3. Run `gitman doctor` (see the [CLI reference](../reference/cli.md)) and check
   the web and worker logs. Sign in, open a repository, and confirm its history,
   run logs, secrets and deployment records before resuming.
4. `gitman admin maintenance disable`.

If the new version fails, restore the backup into a fresh database and data
directory with the previous Gitman version, then point the service at it. Do not
try to reverse a migration or run an older binary against the newer schema.

**Upgrade promise:** in-place upgrades are supported from any 1.0 release
candidate to 1.0 final, and between later 1.x releases, as long as the release
notes do not say otherwise. Beta schemas are not kept compatible with earlier
betas; restore a matching backup when moving between beta versions.

## Schema changes

The schema has one current baseline and ordinary versioned migrations. There are
no legacy schema detectors, adapters or compatibility migrations. Test subsequent
schema changes against a restored snapshot before replacing the running version.
