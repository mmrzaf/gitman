# Troubleshooting

## Web health

```bash
curl -f http://localhost:8080/health
```

A healthy server returns JSON with `"status":"ok"`.

## CI run is skipped

The selected commit does not contain a root-level `.gitman-ci.yml`.

## CI image is unavailable

Gitman uses `docker run --pull never`. Pre-pull or build the exact configured image on the runner host:

```bash
docker pull debian:bookworm-slim
```

## Dependencies cannot download

The default CI network mode is `none`. Put dependencies into the image, repository, or warmed `/gitman/cache`, or deliberately change `GITMAN_CI_NETWORK` after reviewing the security impact.

## Worker rejects container user

`GITMAN_CI_CONTAINER_USER` must be a numeric non-root `UID:GID`, for example:

```bash
export GITMAN_CI_CONTAINER_USER=1000:1000
```

## Worker cannot access Docker socket

For Compose on Linux, set the socket group ID before starting the stack:

```bash
export DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
docker compose up -d
```

Then inspect:

```bash
docker compose logs -f worker
```

## Dockerized worker bind-mount errors

When the worker runs in Docker, both path prefixes must be set together and must be absolute:

```text
GITMAN_CI_WORKER_PATH_PREFIX=/data
GITMAN_CI_HOST_PATH_PREFIX=<absolute host data directory>
```

The included Compose file configures them.

## Cannot save CI secrets

Set the same non-empty `GITMAN_SECRET_KEY` for web and worker. Preserve the value externally. If the key changed, previously stored values cannot be decrypted.

## CI log stops growing

The default log limit is 10 MiB. Gitman appends a suppression notice and discards further output after the limit is reached.

## SSH clone fails

Check:

1. The host OpenSSH account exists.
2. `/home/git/.ssh/authorized_keys` points to Gitman's generated file.
3. File ownership matches the host `git` account.
4. `GITMAN_BINARY_PATH` points to a working host wrapper.
5. The wrapper exports the correct `GITMAN_DB` and `GITMAN_REPOS` paths.
6. The user added a valid SSH public key in the UI.

## Clone links show the wrong host

Set:

```bash
export GITMAN_PUBLIC_URL=https://git.example.com
export GITMAN_SERVER_HOST=git.example.com
```

`GITMAN_PUBLIC_URL` controls HTTP clone links. `GITMAN_SERVER_HOST` controls SSH clone links.

## Reverse-proxy login problems

Set secure-cookie and proxy trust settings only behind a trusted HTTPS proxy:

```bash
export GITMAN_FORCE_SECURE_COOKIES=true
export GITMAN_TRUST_PROXY_HEADERS=true
```

Ensure the proxy forwards the original HTTPS scheme.
