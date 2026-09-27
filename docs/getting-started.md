# Getting started

Gitman runs with Docker Compose on a host that already runs Traefik and
PostgreSQL in Docker. See [Docker deployment](operator/docker.md) for the
full picture; this is the quickest path to a running instance.

## Prerequisites

- Docker with the Compose plugin.
- Traefik on an external Docker network named `proxy`.
- PostgreSQL reachable as `postgres` on an external Docker network named
  `data`, with a database and user for Gitman.

## Steps

```sh
docker build --build-arg VERSION=v1.0.0-beta.21 -t gitman:1.0.0-beta.21 .
cp .env.example .env            # then fill it in — see operator/configuration.md
sudo install -d -o 1000 -g 1000 /srv/apps/gitman/data
docker compose up -d
```

`.env` needs at least `GITMAN_IMAGE`, `GITMAN_DOMAIN`,
`GITMAN_DATABASE_URL` and `GITMAN_DATA_DIR`. See [Configuration
reference](operator/configuration.md) for every setting.

Create the first admin; the command prints their generated password:

```sh
docker compose exec web gitman admin person add --admin darius
```

Sign in at `https://<GITMAN_DOMAIN>`, create a repository from Home, and
create an access token for yourself under **Access tokens** (in the menu
under your username, top right of every page). Git uses your username and
that token as the password:

```sh
git clone https://git.example.com/waiotech.git
```

A read token can clone and fetch; pushing needs a write token.

Gitman never pulls Docker images itself — every image a pipeline uses
(its `image`, and anything in `requires`) must already exist on the
worker's Docker host before a run needs it.
