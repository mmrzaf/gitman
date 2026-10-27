# Troubleshooting

## A run stays queued

A queued run waits for a worker to claim it. When no worker is ready,
the run's page, Home and the output of `git push` say so: start one
(`gitman worker`, or `make run-worker` from a checkout), and it claims
the run at once. A worker counts as ready while it is not stopped, has
heartbeated and rechecked readiness within the last minute, and advertises
every image the run requires. A running run is failed only after five
minutes without a heartbeat; readiness and loss deliberately use different
thresholds.

If a worker is ready but the run still waits, a required image is missing.
`gitman admin images missing` names it and the pipelines that require it, and
the **Images** page shows the same under **Missing required images**. Make the
image present on that worker's Docker host; Gitman never pulls it.

## A run is stuck / its worker seems gone

A run whose worker stops sending heartbeats for five minutes is failed
automatically, with that reason recorded. Its step container and
workspace are cleaned up by a live worker on the same host within a few
more minutes — no action needed if another worker is healthy.

If a worker was killed outright (`SIGKILL`, an OOM kill) and nothing else
is running on that host, nothing removes its leftover step containers
and workspace automatically:

```sh
gitman admin worker cleanup
```

Run this by hand, or on a schedule, on hosts where that can happen.

If the host itself is gone for good — destroyed, reinstalled, no longer
reachable — and you have confirmed its containers are stopped, release its
bookkeeping explicitly:

```sh
gitman admin worker release --confirm <worker-id>
```

It refuses a worker that is still heartbeating. It fails that worker's running
runs, releases their deployment-target ownership, marks their containers removed
and stops the worker record. Without it, a lost deployment owner keeps blocking
deployments and its repository blocks backups forever. Never delete database
rows to work around it.

Runs are never retried automatically — re-run a failed run by hand
(**Run again** on the run's page, or push again).

## A repository says it has an unfinished operation

Open **Operations** as an admin. **Retry recovery** completes a recorded
intent when Git and the database allow it. If the same operation keeps failing,
read its recovery error and inspect the repository. When the error is permanent
and the operation has no Git receipt proving it changed refs, an admin can
release the block:

```sh
gitman admin operation abandon --reason "why it cannot be recovered" <operation-id>
```

Abandoning deliberately changes no Git ref and no repository row: it records the
reason in Activity and clears the mutation block so you can repair the
half-finished state. It is refused for an operation recovery has not failed yet,
and for one whose Git receipt exists — run recovery instead.

## After a database outage

A worker waits five minutes of its own healthy heartbeats after a database
outage before it judges any *other* worker lost, to avoid a thundering
herd of runs being marked failed the moment the database comes back.

## Docker daemon unreachable from a worker

A worker whose Docker daemon doesn't answer claims no new runs until it
does — existing runs on that worker still fail per the heartbeat rule
above, but the worker doesn't make things worse by claiming more work it
can't do.

## A pipeline step can't find an image

Gitman never pulls images. A run whose `image` or `requires` image is
missing stays queued, with the run page saying no ready worker advertises
it. Make the image present on the worker's Docker host — pull or build it
there — and the next claim picks the run up. Execution checks again before
the first step, so an image removed between claim and start fails the run
with a message naming it.

## `gitman check .gitman.yml` fails

Read the specific error — it names the exact field. A common one:
pipeline `env` (or a target's `env`) can't set a variable starting with
`GITMAN_`; that prefix is reserved for the variables Gitman injects into
every step. See [Pipeline configuration](ci/configuration.md).

## Sign-in says too many attempts for everyone

Without `GITMAN_TRUSTED_PROXIES`, every request behind a reverse proxy
looks like it comes from the proxy. The sign-in limiter then has one
address budget shared by all users, and twenty failed attempts lock out
sign-in for fifteen minutes. Set `GITMAN_TRUSTED_PROXIES` to the proxy's
actual address or network (the production Compose example defaults to the
Compose network range) and restart web. Gitman logs a warning the first
time it sees a private peer in that state.

## Check an installation

```sh
gitman doctor
```

It only reads: config, data directory and free space, database and schema
version, Docker, and registered workers. Each line says what is wrong and what
to do; warnings alone exit 0, any failure exits 1. It is safe to run beside a
live web or worker. A database schema newer than the binary is a failure —
deploy the matching version or restore the backup.

## Health checks

- `GET /healthz` — is the process up at all. Doesn't touch PostgreSQL.
- `GET /readyz` — also checks PostgreSQL is reachable.

Use `/readyz` for a load balancer's or orchestrator's readiness probe,
and `/healthz` for pure liveness. The worker has no HTTP endpoint; its
container is either running or not, and the heartbeat says whether it is doing
work.

## Large clone or push hangs / times out

This is almost always a reverse proxy's read timeout, not Gitman's — see
[HTTPS proxy configuration](operator/docker.md#reverse-proxy-timeouts).
