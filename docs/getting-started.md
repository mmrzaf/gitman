# Getting started

The supported install path is Docker Compose, behind a Traefik that
already serves other sites on the host. See
[Docker deployment](operator/docker.md) for the full picture; this is the
quickest path to a running instance.

## Prerequisites

- Docker with the Compose plugin.
- A Traefik instance already running on the host, with an external
  Docker network it and Gitman will share.

## Steps

```sh
cp .env.example .env            # then fill it in — see operator/configuration.md
sudo install -d -o 1000 -g 1000 /srv/gitman
docker compose up -d --build
```

`.env` needs at least `GITMAN_HOST` (the hostname Traefik routes to
Gitman) and `POSTGRES_PASSWORD`. See
[Configuration reference](operator/configuration.md) for every setting.

Create the first admin; the command prints their generated password:

```sh
docker compose exec web gitman admin person add --admin darius
```

Sign in at `https://<GITMAN_HOST>`, create a repository from Home, and
create an access token for yourself under **Access tokens** (in the menu
under your username, top right of every page). Git uses your username and
that token as the password:

```sh
git clone https://git.example.com/waiotech.git
```

A read token can clone and fetch; pushing needs a write token.

## Scaling workers

```sh
docker compose up -d --scale worker=3
```

Gitman never pulls Docker images itself — every image a pipeline uses
(its `image`, and anything in `requires`) must already exist on the
worker's Docker host before a run needs it.
