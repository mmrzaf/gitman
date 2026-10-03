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

Use an empty database and data directory for a new installation. See
[Configuration](configuration.md) and [Backups and recovery](backups-and-upgrades.md).
