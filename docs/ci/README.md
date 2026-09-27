# Pipelines

Gitman's pipelines are the built-in CI/CD: a repository's pipeline is
`.gitman.yml` at the root of the commit being run. There's no separate CI
service to wire up — a push that a [ref rule](../operator/security.md)
allows to run the pipeline queues a run, a worker claims it, and each
step runs in its own Docker container on the worker's host.

- [Configuration reference](configuration.md) — the full `.gitman.yml`
  schema.
- [Secrets](secrets.md) — how repository secrets reach a run.

## What a pipeline can and can't do

A pipeline is one commit, run once, in order: the first failing step ends
the run and later steps are skipped. There are deliberately no automatic
retries, no caches shared between runs, no artifacts, no matrix builds
and no scheduled runs (see
[Not added, on purpose](../../README.md#not-added-on-purpose)). Gitman
never pulls images — every image a pipeline references (its `image`, and
anything in `requires`) must already exist on the worker's Docker host.

## Checking a pipeline before pushing

```sh
gitman check .gitman.yml
```

This validates the file against the same rules the worker enforces (used
by this repository's own [`.gitman.yml`](../../.gitman.yml)), including
that step `env` never sets a `GITMAN_`-prefixed variable — that prefix is
reserved for the variables Gitman itself injects into every step.
