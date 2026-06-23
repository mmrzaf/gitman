# Docker deployment

The included Compose stack defines:

| Service | Purpose | Privilege note |
| --- | --- | --- |
| `web` | Browser UI and Git smart HTTP | No Docker socket |
| `worker` | Optional built-in CI executor | Mounts `/var/run/docker.sock` and controls sibling job containers |

Both services share a host data directory mounted at `/data`.

## Web-only deployment

Use this when CI is not required:

```bash
export GIT_UID=$(id -u)
export GITMAN_DATA_DIR="$(pwd)/data"
mkdir -p "$GITMAN_DATA_DIR"
chmod 700 "$GITMAN_DATA_DIR"
docker compose up -d --build web
```

## Web plus CI worker

Use a Linux Docker host. Determine the Docker socket group ID so the non-root worker can access the daemon:

```bash
export GIT_UID=$(id -u)
export DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
export GITMAN_DATA_DIR="$(pwd)/data"
mkdir -p "$GITMAN_DATA_DIR"
chmod 700 "$GITMAN_DATA_DIR"
docker compose up -d --build
```

Pre-pull approved job images on the Docker host:

```bash
docker pull golang:1.26-alpine
```

## Create the first account

```bash
read -rsp 'Admin password: ' ADMIN_PASSWORD; printf '\n'
printf '%s\n' "$ADMIN_PASSWORD" | docker compose exec -T web gitman admin users create admin
unset ADMIN_PASSWORD
```

## Bind address

Compose publishes the web service to `127.0.0.1:8080` by default. Change the host bind only when the service is intentionally exposed:

```bash
GITMAN_BIND_ADDRESS=0.0.0.0 docker compose up -d
```

Prefer a reverse proxy that listens publicly and forwards to `127.0.0.1:8080`.

## CI secrets

Generate and persist an encryption key in your deployment secret manager:

```bash
export GITMAN_SECRET_KEY=$(openssl rand -hex 32)
docker compose up -d
```

The same value must reach both `web` and `worker`.

## HTTPS reverse proxy

Terminate TLS at a trusted reverse proxy and configure:

```bash
export GITMAN_PUBLIC_URL=https://git.example.com
export GITMAN_FORCE_SECURE_COOKIES=true
export GITMAN_TRUST_PROXY_HEADERS=true
docker compose up -d
```

The proxy must set `X-Forwarded-Proto: https` or an equivalent standardized `Forwarded` header. Do not enable proxy-header trust when clients can bypass the trusted proxy.

## Compose-specific host variables

Compose adapts a few host-side variable names before passing values to the worker:

| Host-side Compose variable | Worker environment variable |
| --- | --- |
| `GITMAN_CI_MEMORY_LIMIT` | `GITMAN_MEMORY_LIMIT` |
| `GITMAN_CI_CPU_LIMIT` | `GITMAN_CPU_LIMIT` |

Use the left column when invoking Docker Compose. Use the right column for direct worker deployments.

## Operations

```bash
docker compose ps
docker compose logs -f web
docker compose logs -f worker
docker compose restart web worker
docker compose down
```

Read [security](security.md), [configuration](configuration.md), and [backups and upgrades](backups-and-upgrades.md) before production use.
