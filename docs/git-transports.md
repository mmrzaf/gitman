# Git transports

Gitman supports Git over HTTPS only — the smart HTTP protocol
(`git-upload-pack`/`git-receive-pack`), served by the `web` process. There
is no SSH transport; this is a deliberate scope decision (see
[Not added, on purpose](../README.md#not-added-on-purpose)), not a gap.

## Authentication

Use your Gitman username and an access token (from **Access tokens**, or
`gitman admin token create`) as the HTTP password:

```sh
git clone https://git.example.com/example-app.git
Username: alice
Password: <access token>
```

Most Git clients and credential helpers cache this the same way they
would a personal access token on any other Git host.

- A **read** token can clone and fetch.
- A **write** token can also push.

## Push behavior

A push is rejected **before changing Git refs or creating a durable
operation** when a ref rule forbids it (who may push, whether force-push or
deletion is allowed), or when its updates are invalid. Gitman rejects the
whole push rather than partially accepting a batch.

Branches and tags live in separate Git namespaces, but names *within the
same namespace* cannot be both a parent and a child: `topic` and
`topic/child` cannot both be branches. Even deleting `topic` while creating
`topic/child` in the same atomic push is rejected by Git's files ref backend;
do those as **two separate pushes**. Duplicate updates to one ref, unchanged
old/new object IDs and mixed SHA-1/SHA-256 object IDs are also rejected.
These checks prevent malformed pushes from leaving an unfinishable repository
operation.

If a ref rule enables a pipeline, `web`'s `proc-receive` hook queues a run
and the push output prints a link to it.

### After an interrupted push

Gitman first records a durable push intent, then atomically applies all Git
ref changes together with a receipt, then commits its database push record,
ref index and queued runs. A crash or PostgreSQL outage between the last two
steps can cause Git to tell the client the push did not finish even though the
refs were already changed. **Do not automatically force-push or repeat a
possibly completed deployment.** Gitman replays the receipt during recovery,
records the missing database rows once, and never executes pipeline steps as
part of that recovery. If reconciliation fails, the operation remains visible
and new mutations to that repository are blocked until an operator resolves
it. See [Recovery](operator/backups-and-upgrades.md#restore).

## Large clones and pushes

If Gitman sits behind a reverse proxy (see
[Docker deployment](operator/docker.md)), a large clone or push can
outlast the proxy's own read timeout before Gitman's smart-HTTP handler
finishes. Raise the proxy's timeout for Gitman's route rather than
Gitman's own configuration, which has no separate knob for this.
