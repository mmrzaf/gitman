# Docker deployment

The standalone Compose configuration includes PostgreSQL, web, a worker,
and initialization of the data directory. It needs no external Docker
networks or pre-existing reverse proxy. Copy `.env.example` to `.env`, set
a database password and an absolute host data directory, then run:

```sh
docker compose up --build -d
docker compose exec web gitman admin person add --admin darius
```

Web listens on `127.0.0.1:8080` by default. PostgreSQL is published only on
loopback, at configurable `GITMAN_DATABASE_PORT` (default 5433). The web
health check uses readiness; workers wait for web and database readiness.

For public HTTPS, set `GITMAN_DOMAIN` to a domain pointing to this server:

```sh
docker compose -f compose.yaml -f compose.production.yaml up --build -d
```

The production example adds Caddy on ports 80 and 443. Its certificate and
configuration storage persist in named volumes. Keep the app's published
port bound to loopback. When you configure trusted proxies for client-IP
forwarding, use the actual proxy addresses or network; do not trust all
addresses.

Workers fetch from `http://web:8080`. Service names stay within the Compose
project; there are no fixed container names or external network names.
Scale workers with `docker compose up -d --scale worker=3`.

Every worker controls the host Docker socket and is root-equivalent on that
host. Pipeline images and required images must already be present there;
Gitman never pulls them. Ordinary step limits apply to the step container,
not containers a socket-enabled step creates. The data path is mounted at
the identical absolute path inside and outside the worker because step
containers bind-mount workspaces by host path.

The worker gets an explicit environment rather than the whole `.env`, so the
database password variable is not handed to a root process that also holds the
Docker socket. The database publishes only on loopback (`127.0.0.1:5433` by
default) for host-side `pg_dump`/`psql`; web and worker use the Compose network.
Compose applies log rotation, memory limits (`GITMAN_WEB_MEMORY_LIMIT`,
`GITMAN_WORKER_MEMORY_LIMIT`, `GITMAN_DATABASE_MEMORY_LIMIT`,
`GITMAN_PROXY_MEMORY_LIMIT`) and health checks to the web and proxy. The worker
has no HTTP endpoint: its liveness is its process, and Compose restarts it if
that exits. A worker that stops heartbeating has its runs failed after five
minutes even if the container itself is gone.

## Disk use and images

Every pipeline run executes in images that must already exist on the worker
host, and steps that build images leave them there. Gitman neither pulls nor
prunes images: `docker image prune` (and build-cache pruning) on the worker host
is the operator's job. Pruning images a pipeline still requires is safe only
after updating the pipeline or its `requires`; a run stays queued until a ready
worker advertises all of them.

The Gitman image does not include PostgreSQL client tools. `admin maintenance
backup` and `gitman restore` therefore run on a host with a `pg_dump`/
`pg_restore` matching the database major version; see
[Backups and upgrades](backups-and-upgrades.md).

## Reverse proxy timeouts

Clones, pushes and archive downloads stream for as long as the transfer takes,
and a Git HTTP request may run for an hour. If a reverse proxy in front of
Gitman has a short read or write timeout, a large transfer is cut off even
though Gitman is healthy. Raise the proxy's timeouts for the Git routes, or
disable request buffering and let Gitman stream. Caddy's defaults are generous;
an nginx `proxy_read_timeout` or `proxy_send_timeout` of a few minutes is not.

Use an empty database and data directory for a new installation. See
[Configuration](configuration.md) and [Backups and recovery](backups-and-upgrades.md).
