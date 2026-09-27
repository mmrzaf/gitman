# Troubleshooting

## A run stays queued

A queued run waits for a worker to claim it. When no worker is online,
the run's page, Home and the output of `git push` say so: start one
(`gitman worker`, or `make run-worker` from a checkout), and it claims
the run at once. A worker counts as online from when it starts until it
shuts down, or until it has sent no heartbeat for five minutes.

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
Runs are never retried automatically — re-run a failed run by hand
(**Run again** on the run's page, or push again).

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

Gitman never pulls images. If a run fails because an image (the
pipeline's `image`, or one listed in `requires`) is missing, that image
needs to be present on the worker's Docker host already — pull or build
it there, then re-run.

## `gitman check .gitman.yml` fails

Read the specific error — it names the exact field. A common one:
pipeline `env` (or a target's `env`) can't set a variable starting with
`GITMAN_`; that prefix is reserved for the variables Gitman injects into
every step. See [Pipeline configuration](ci/configuration.md).

## Health checks

- `GET /healthz` — is the process up at all. Doesn't touch PostgreSQL.
- `GET /readyz` — also checks PostgreSQL is reachable.

Use `/readyz` for a load balancer's or orchestrator's readiness probe,
and `/healthz` for pure liveness.

## Large clone or push hangs / times out

This is almost always a reverse proxy's read timeout, not Gitman's — see
[Behind Traefik](operator/docker.md#behind-traefik).
