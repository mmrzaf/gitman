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
deployment owners and repository operations are resolved. Check **Workers** and
**Operations**, or `admin worker list` and `admin operation list`. Recover pending
operations with `admin operation recover`. For a lost worker, perform
`admin worker cleanup` on its Docker host with its original workspace path.
Never release target ownership by deleting database rows.

`backup` holds an exclusive admission lease, so another process cannot resume
writes during the snapshot. It requires a new destination outside the data
directory and writes `database.dump`, `repos.tar.gz` and a final checksummed
`manifest.json`. Failed attempts do not have a valid final manifest. PostgreSQL's
`pg_dump` and `pg_restore` must be installed where these commands run, with a
client version matching or newer than the database server. The Gitman image does
not include PostgreSQL tools. Run backup and restore using the native Gitman
binary on a host with those tools and access to the database and repository data.

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
expire; collection reconciles pins after database retention. Monitor disk space:
the configured free reserve pauses pushes and claims (5 GiB by default), and
active executions enforce a configured workspace budget (10 GiB by default). These are monitored limits, not filesystem quotas.

## Schema changes

The schema has one current baseline and ordinary versioned migrations. There are
no legacy schema detectors, adapters or compatibility migrations. Test subsequent
schema changes against a restored snapshot before replacing the running version.
