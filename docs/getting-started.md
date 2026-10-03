# Getting started

The standalone Compose setup includes PostgreSQL, web and a worker.
See [Docker deployment](operator/docker.md) for public HTTPS and scaling.

## Prerequisites

Docker with the Compose plugin, and local images for your pipelines.

## Steps

```sh
cp .env.example .env
# Set a database password and an absolute data directory in .env.
docker compose up --build -d
```

Start with an empty database and data directory; migrations apply the current schema.

Create the first admin; the command prints their generated password:

```sh
docker compose exec web gitman admin person add --admin darius
```

Sign in at `http://localhost:8080` (or your configured public HTTPS domain),
change the temporary password, create a repository from Home, and
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
