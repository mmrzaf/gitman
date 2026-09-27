# Docker deployment

The supported deployment is Docker Compose, on a host that already runs
Traefik and PostgreSQL in Docker. `compose.yaml` defines two services:

| Service | What it does | Networks | Privileges |
| --- | --- | --- | --- |
| `web` (`gitman-web`) | The web interface and Git over HTTPS. | `proxy`, `data`, `gitman_internal` | uid 1000, no capabilities, read-only root filesystem. |
| `worker` (`gitman-worker`) | Runs pipelines: each step in its own container on the host's Docker. | `data`, `gitman_internal` | root, with the host's Docker socket. |

Both read their settings from `.env` next to `compose.yaml`.

## Prerequisites

On the host, outside this repository:

- Docker with the Compose plugin. Nothing needs the internet: images
  are built or loaded on the host, and builds use the mirrors their build
  arguments name.
- Traefik, on an external Docker network named `proxy`, with an entry
  point named `websecure`, TLS, and the file-provider middleware
  `security-headers@file`.
- PostgreSQL, reachable as `postgres` on an external Docker network
  named `data`, with a database and a user for Gitman:

  ```sql
  CREATE USER gitman PASSWORD '...';
  CREATE DATABASE gitman OWNER gitman;
  ```

## Setup

The image comes from one of three places:

- **Gitman itself**, once it runs: its own pipeline (`.gitman.yml`)
  builds `gitman:<version>` on the host on every `v*` tag, given a ref
  rule for `v*` tags with **run** and **Docker**.
- **A release**, for the first install. A host with internet access
  pulls `ghcr.io/mmrzaf/gitman:<version>`. A host without it loads the
  Docker archive attached to the GitHub release, copied over:
  `docker load < gitman-docker-image-linux-amd64-<version>.tar.gz`, which
  gives `gitman:<version>`.
- **By hand**, from a checkout of the tag. Behind mirrors, pass the same
  build arguments `.gitman.yml` does:

  ```sh
  docker build --build-arg VERSION=v1.0.0-beta.21 -t gitman:1.0.0-beta.21 .
  ```

Then, in the deployment directory (for example `/srv/apps/gitman`), with
`compose.yaml` and a `.env` made from `.env.example`:

```sh
sudo install -d -o 1000 -g 1000 /srv/apps/gitman/data
docker compose up -d
docker compose exec web gitman admin person add --admin darius
```

Migrations run whenever web or the worker starts. See [Configuration
reference](configuration.md) for every `.env` setting.

## Behind Traefik

- `GITMAN_TRUSTED_PROXIES` must cover every proxy between the client and
  Gitman, or every request appears to come from the nearest untrusted
  one and the sign-in limiter counts everyone together. Gitman reads
  `X-Forwarded-For` from the right, skipping trusted proxies, and takes
  the first address that isn't one. For Traefik alone that is Docker's
  networks, `172.16.0.0/12`. With a CDN such as ArvanCloud in front of
  Traefik, add the CDN's published ranges too, comma-separated, and have
  Traefik trust them as well (`forwardedHeaders.trustedIPs` on the entry
  point); otherwise the client is the CDN's edge.
- Clones and pushes stream, and can be large and slow. If the `websecure`
  entry point sets a read timeout, raise it for Gitman, for example
  `--entryPoints.websecure.transport.respondingTimeouts.readTimeout=0`.
  A CDN in front of Traefik must not cache Gitman or cap request size.

## The data directory

`GITMAN_DATA_DIR` is bind-mounted into `web` and `worker` at the
**identical path**. A pipeline step container is started by the *host's*
Docker daemon, so the worker hands Docker a host path for the step's
workspace, and must see that workspace at the same path the host does.
Don't map it to a different path on either side.

## Docker access for pipelines

`worker` always has the host's Docker socket, but a pipeline only gets to
use it (`docker: true` in `.gitman.yml`) on refs whose rule allows it:

```sh
docker compose exec web gitman admin rule set --docker --run myrepo branch main
```

That hands the ref's pipeline the same power as anyone with the host's
Docker socket: root, effectively. Grant it only on refs whose pushers
you'd trust with that. See [Security model](security.md).

Gitman never pulls images: every image a pipeline uses must already
exist on the host before a run needs it.

## Mirrors

Every download the image build makes is a build argument: `GO_IMAGE`,
`RUNTIME_IMAGE`, `DOCKER_CLI_IMAGE`, `DEBIAN_MIRROR`,
`DEBIAN_SECURITY_MIRROR` and `GOPROXY`. See the top of the `Dockerfile`;
Gitman's own `.gitman.yml` builds it with Liara's mirrors.

## Operations

```sh
docker compose ps
docker compose logs -f web worker
docker compose restart web worker
docker compose down
```

Read [Security model](security.md) and [Backups and
upgrades](backups-and-upgrades.md) before running it in production.
