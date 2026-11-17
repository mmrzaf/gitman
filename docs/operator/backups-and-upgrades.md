# Backups, recovery and future upgrades

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

## New installation

Create a new, empty PostgreSQL database and data directory. Gitman initializes
its schema on startup. Create an administrator and configure repositories,
permissions, and tokens using the normal CLI or web interface.

For recoverable data, use the maintenance backup and restore process above.
Do not copy bare repository directories independently of their database
metadata: their internal IDs are linked to database records.

## Upgrading

Versioned migrations and advisory locking apply any new schema changes.
When upgrading a supported installation:

1. Take a consistent maintenance backup with the encryption key kept separately.
2. Resolve active work, then replace the binary/image and restart web and worker.
   Pending migrations apply under the shared PostgreSQL advisory lock.
3. Check `gitman doctor`, browser access, Git refs, secrets, history and worker
   readiness before resuming operations.
4. If verification fails, restore the backup **with its matching old binary**
   into a separate, empty database and data directory. Do not reverse SQL
   migrations in place.

Applied migrations have stable filenames and SQL fingerprints; never edit one
that has shipped. Add a new numbered migration for subsequent schema changes
and test it against a restored snapshot before deployment.
