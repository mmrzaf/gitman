# Docker deployment

The supported deployment is Docker Compose, behind a Traefik instance
already running on the host. `compose.yaml` defines three services:

- **`postgres`** — `postgres:16-alpine` (or `POSTGRES_IMAGE`), on an
  internal-only network, with a health check Compose uses to gate `web`
  and `worker` startup.
- **`web`** — built from the repository's `Dockerfile`, joined to both the
  internal network (to reach `postgres`) and the external Traefik
  network. Routing is entirely via container labels
  (`traefik.http.routers.gitman...`) — there's no separate Traefik
  config file to maintain.
- **`worker`** — the same image as `web`, run as `root` so it can reach
  the host's Docker socket (`/var/run/docker.sock`, bind-mounted in), and
  started with `command: ["worker"]`.

`web` and `worker` share one environment block (a YAML anchor in
`compose.yaml`), so there's one place to change a setting for both.

## Prerequisites

- Docker with the Compose plugin.
- A Traefik instance on the host, with an external network
  (`TRAEFIK_NETWORK`, default `traefik`) that Traefik and Gitman both
  join.

## Setup

```sh
cp .env.example .env            # fill in GITMAN_HOST, POSTGRES_PASSWORD, ...
sudo install -d -o 1000 -g 1000 /srv/gitman
docker compose up -d --build
docker compose exec web gitman admin person add --admin darius
```

See [Configuration reference](configuration.md) for every `.env` setting.

## The data directory

`GITMAN_DATA_DIR` (host-side, default `/srv/gitman`) is bind-mounted into
`web` and `worker` at the **identical path**. This matters because a
pipeline step container is started by the *host's* Docker daemon, not by
the worker container's own filesystem view — the worker hands Docker a
host path for the step's workspace mount, so the worker must see that
workspace at the same path the host does. Don't remap this mount to a
different path on either side.

## Docker access for pipelines

`worker` always has the host's Docker socket available, but a pipeline
only gets to use it (`docker: true` in `.gitman.yml`) on refs whose rule
explicitly allows it:

```sh
gitman admin rule set --docker --run myrepo branch main
```

Granting this hands that ref's pipeline the same privileges as anyone
with access to the host's Docker socket — root, effectively. Grant it
only on refs whose pushers you'd trust with that. See
[Security model](security.md).

## Scaling workers

```sh
docker compose up -d --scale worker=3
```

Workers coordinate purely through PostgreSQL (row locking to claim runs,
`LISTEN`/`NOTIFY` for wake-ups) — there's no other state to share, so
this is safe to do at any time. Gitman never pulls images; every image a
pipeline references must already exist on the worker host before a run
needs it.

## Mirrors

Every download the image build makes is a build argument
(`GO_IMAGE`, `RUNTIME_IMAGE`, `GOPROXY`, `ALPINE_MIRROR` — see the top of
the `Dockerfile`). Compose's own images (`POSTGRES_IMAGE`,
`GITMAN_IMAGE`) are set in `.env` if you need a registry mirror for
those too.

## Behind Traefik

- Set `GITMAN_TRUSTED_PROXIES` to the address range Traefik reaches
  Gitman from (the default, `172.16.0.0/12`, covers Docker's default
  bridge networks) — otherwise every request appears to come from
  Traefik, and the sign-in rate limiter treats every client as one.
- Large clones and pushes can outlast a proxy's read timeout. If your
  Traefik entry point sets one, raise it for Gitman's route, e.g.
  `--entryPoints.websecure.transport.respondingTimeouts.readTimeout=0`.
