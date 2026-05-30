# Gitman Docker setup

The Compose stack runs two containers:

- `web`: browser UI and Git smart HTTP server.
- `worker`: optional built-in CI executor. It launches restricted job containers through the host Docker socket.

Both containers share `./data` by default.

## Local HTTP setup

Requirements:

- Docker Engine with the Compose plugin.
- Access to `/var/run/docker.sock` when CI is enabled.

Start the stack:

```bash
export GIT_UID=$(id -u)
export DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
export GITMAN_DATA_DIR="$(pwd)/data"
mkdir -p "$GITMAN_DATA_DIR"
chmod 700 "$GITMAN_DATA_DIR"
docker compose up -d --build
```

Create the first user:

```bash
read -rsp 'Admin password: ' ADMIN_PASSWORD; printf '\n'
printf '%s\n' "$ADMIN_PASSWORD" | docker compose exec -T web gitman admin users create admin
unset ADMIN_PASSWORD
```

Open `http://localhost:8080`.

The default bind address is `127.0.0.1`. Change it only when the service is intentionally exposed:

```bash
GITMAN_BIND_ADDRESS=0.0.0.0 docker compose up -d
```

## CI runner images

Gitman starts CI containers with `--pull never`. Pre-pull approved images on the Docker host before triggering jobs:

```bash
docker pull golang:1.24-alpine
```

This prevents repository-controlled CI configuration from pulling arbitrary images into Docker storage. Set `GITMAN_CI_CONTAINER_USER` only when a numeric non-root UID:GID override is required; otherwise the worker uses its own numeric non-root UID:GID. Running the CI worker as root is rejected.

## CI secrets

Set an encryption key before storing CI secrets:

```bash
export GITMAN_SECRET_KEY=$(openssl rand -hex 32)
docker compose up -d
```

Persist that value in your deployment secret manager. Losing it makes stored CI secrets unreadable. Leaving it empty disables CI-secret storage.

## HTTPS reverse proxy

Terminate TLS at a reverse proxy and forward traffic to `127.0.0.1:8080`. Configure:

```bash
export GITMAN_PUBLIC_URL=https://git.example.com
export GITMAN_FORCE_SECURE_COOKIES=true
export GITMAN_TRUST_PROXY_HEADERS=true
docker compose up -d
```

The proxy must set `X-Forwarded-Proto: https` or the equivalent standardized `Forwarded` header. Do not enable `GITMAN_TRUST_PROXY_HEADERS` when requests can bypass the trusted proxy.

## SSH Git transport

HTTP Git works without host changes. SSH Git transport uses the host OpenSSH server, a host-installed Gitman binary, and Gitman's generated `authorized_keys` file. The host Git user UID must match the container `GIT_UID` so OpenSSH accepts the generated file ownership.

1. Create or identify a dedicated host account and align the container UID:

   ```bash
   sudo useradd --create-home --shell /usr/sbin/nologin git 2>/dev/null || true
   export GIT_UID=$(id -u git)
   sudo install -d -m 700 -o git -g git /home/git/.ssh
   sudo install -d -m 700 -o git -g git "$(pwd)/data"
   docker compose up -d --build
   ```

2. Install the same Gitman binary on the host:

   ```bash
   docker compose cp web:/usr/local/bin/gitman /tmp/gitman
   sudo install -m 755 /tmp/gitman /usr/local/bin/gitman
   ```

3. Point the host account at Gitman's generated key file:

   ```bash
   sudo ln -sfn "$(pwd)/data/authorized_keys" /home/git/.ssh/authorized_keys
   sudo chown -h git:git /home/git/.ssh/authorized_keys
   ```

4. Install a host wrapper. Replace `/srv/gitman` with the absolute directory containing `data/`:

   ```bash
   sudo tee /usr/local/bin/gitman-wrap >/dev/null <<'SCRIPT'
   #!/bin/sh
   export GITMAN_DB=/srv/gitman/data/db/gitman.sqlite
   export GITMAN_REPOS=/srv/gitman/data/repos
   export GITMAN_ARTIFACTS=/srv/gitman/data/artifacts
   export GITMAN_AUTH_KEYS=/srv/gitman/data/authorized_keys
   exec /usr/local/bin/gitman "$@"
   SCRIPT
   sudo chmod 755 /usr/local/bin/gitman-wrap
   ```

5. Keep the Compose default:

   ```bash
   GITMAN_BINARY_PATH=/usr/local/bin/gitman-wrap
   ```

When users add SSH keys in the UI, Gitman atomically regenerates `data/authorized_keys` with forced commands that call the host wrapper. The wrapper does not need Docker socket access.

## Worker privilege boundary

The worker mounts `/var/run/docker.sock`. Access to that socket is effectively host-level privilege. CI containers use Docker labels for crash reconciliation, `--pull never`, `--log-driver none`, an explicit numeric UID:GID, and serialized per-repository cache writes. The included limits reduce accidental and repository-driven resource exhaustion, but they are not a substitute for kernel-enforced filesystem quotas or a dedicated runner host.

For stronger isolation:

- Run `worker` on a dedicated machine or VM.
- Put Docker storage and Gitman data on filesystems with quotas.
- Keep `GITMAN_CI_NETWORK=none` unless outbound access is explicitly required.
- Keep worker concurrency low.
- Pre-pull only approved CI images.
- Treat CI logs and artifacts as member-only build data; public repository source does not make them public.

## Backups

Create a full backup from the web container:

```bash
docker compose exec -T web gitman admin repos backup-all /data/backups/gitman-$(date +%F)
```

The destination must be absent or empty and must not be inside `/data/repos` or `/data/artifacts`. The SQLite database is copied coherently with `VACUUM INTO`. Repositories and artifacts are copied live, so use a maintenance window or filesystem snapshot when strict point-in-time consistency is required.

## Common operations

```bash
docker compose ps
docker compose logs -f web
docker compose logs -f worker
docker compose restart web worker
docker compose down
```

Upgrade by backing up first, then rebuilding:

```bash
docker compose exec -T web gitman admin repos backup-all /data/backups/pre-upgrade-$(date +%F-%H%M%S)
docker compose up -d --build
```