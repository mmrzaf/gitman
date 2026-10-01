# Architecture

Gitman is one Go binary with web, worker, admin and restore commands. PostgreSQL
stores accounts, credentials, grants, indexed refs, operation intents, pushes,
runs, logs, deployment ownership and encrypted secrets. Bare repositories live
under `GITMAN_DATA_DIR/repos`; workers use `workspaces` beneath the same data root.

## Durable mutations

The Git `proc-receive` hook validates a bounded push and persists its intent before
changing public refs. One Git ref transaction changes the refs, pins retained
objects and writes a receipt. A PostgreSQL transaction records the exact push,
updates the read model, creates runs and completes the intent. Recovery inspects
the Git receipt and finishes recording once; it never guesses whether a push
was applied. Pending operations block new mutations of that repository.

Create, delete and default-branch changes use the same journal and filesystem
locks. Startup and periodic recovery reconcile interrupted operations. Unknown
repositories are quarantined after locking and rechecking their database state.
One web process owns the repository root; a portable instance marker pairs
storage with the database. Maintenance admission coordinates web, hooks, admin and worker claims.

## Execution and deployment

Workers wake through PostgreSQL LISTEN/NOTIFY with periodic polling. Claims
persist deadlines and are routed by each ready worker's local image inventory.
Images are never pulled automatically. Containers have CPU, memory, PID and
output limits; workspaces and free space are monitored. Container names and IDs
are recorded before start; exited containers retain receipts until recording
commits. Cleanup requires confirmed termination and execution locks.

Workers sharing a Docker engine must use the same absolute workspace root.
Heartbeat loss fails run metadata, but does not release uncertain deployment
ownership. Recovery waits through a database-outage grace period, stops retained
containers and records interruption without rerunning scripts. Only a successful
explicit `type: deploy` step records a deployment. Target ownership is
serialized across workers and released with a durable receipt or confirmed stop.
A deployment record describes script success, not application health.

## Read paths

Server-rendered pages use typed partial views and database read models. Home is
permission-filtered: its repository list is paginated, while deployments,
attention and activity cover all readable repositories. Git
comparisons are batched; missing objects remain unknown. Activity uses stable
keyset cursors. SSE subscriptions have per-person/global limits and repeatedly
check credentials and repository access. Live requests return only named HTML
regions; refreshes are coalesced and bounded. Logs and downloads are streamed.

The supported worker profile is trusted: access to its Docker socket is equivalent
to root on the host. Resource limits do not make untrusted pipelines safe. See
[security](operator/security.md), [configuration](operator/configuration.md) and
[backup recovery](operator/backups-and-upgrades.md).
