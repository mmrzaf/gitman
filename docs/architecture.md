# Architecture

Gitman is a single binary (`cmd/gitman`) that runs as one of a few
processes, selected by its first argument:

- **`gitman web`** serves Git over HTTP (the smart HTTP protocol) and the
  web interface. Git's push hooks (`pre-receive`/`post-receive`) invoke the
  same binary as `gitman hook ...`, generated fresh at every start.
- **`gitman worker`** claims queued pipeline runs from PostgreSQL — woken
  by `LISTEN`/`NOTIFY`, with a periodic poll as a safety net under it —
  fetches each run's commit from `web` over Git HTTP, and runs every
  pipeline step in its own Docker container on the host, using the host's
  Docker socket.
- **`gitman admin ...`** is the operator CLI: people, tokens, repositories,
  ref rules, run cancellation and worker cleanup. See the
  [CLI reference](reference/cli.md).

## State

- **PostgreSQL** holds everything except the repositories themselves:
  people, sessions, tokens, repositories, ref rules, pushes and each ref
  they moved (whether it rewrote history included), pushes Gitman
  refused and why, pipeline runs, deployments, and encrypted secrets.
  `web` and workers share nothing else — no cache, no message queue — so
  any number of workers can run, and PostgreSQL's `LISTEN`/`NOTIFY` plus
  row locking is what lets them coordinate without talking to each other
  directly.
- **Bare Git repositories** live on disk under `GITMAN_DATA_DIR/repos`.
- **Run workspaces** are ephemeral checkouts under
  `GITMAN_DATA_DIR/workspaces`, used only while a run is in progress.
- **Git hook scripts** are regenerated under `GITMAN_DATA_DIR/hooks` on
  every start of `web`, so they never need to survive an upgrade on disk.

## Why one PostgreSQL and no cache

Earlier designs used SQLite and a single web process. The rewrite uses
PostgreSQL specifically so `web` and any number of `worker` processes can
run against the same database without their own coordination layer —
queued runs are claimed with row-level locking, and completion/queue
events are pushed with `LISTEN`/`NOTIFY` instead of polling loops or a
separate broker.

See [Operating it](../README.md#operating-it) in the top-level README for
backup, retention and upgrade behavior, and
[Docker deployment](operator/docker.md) for how `web`, `worker` and
PostgreSQL are wired together in the supported Compose setup.
