# Enable SSH Git transport

HTTP Git works without host SSH changes. SSH transport requires the host OpenSSH server, a dedicated host account, a host-installed Gitman binary, and Gitman's generated `authorized_keys` file.

The host Git user UID must match the container `GIT_UID` so OpenSSH accepts file ownership.

## 1. Create a dedicated host account

```bash
sudo useradd --create-home --shell /usr/sbin/nologin git 2>/dev/null || true
export GIT_UID=$(id -u git)
sudo install -d -m 700 -o git -g git /home/git/.ssh
sudo install -d -m 700 -o git -g git "$(pwd)/data"
docker compose up -d --build
```

## 2. Install the same Gitman binary on the host

```bash
docker compose cp web:/usr/local/bin/gitman /tmp/gitman
sudo install -m 755 /tmp/gitman /usr/local/bin/gitman
```

## 3. Link OpenSSH to Gitman's generated key file

```bash
sudo ln -sfn "$(pwd)/data/authorized_keys" /home/git/.ssh/authorized_keys
sudo chown -h git:git /home/git/.ssh/authorized_keys
```

## 4. Install a host wrapper

Replace `/srv/gitman` with the absolute directory containing `data/`:

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

## 5. Keep the Compose wrapper path

The Compose default is:

```bash
GITMAN_BINARY_PATH=/usr/local/bin/gitman-wrap
```

When users add or remove keys, Gitman atomically regenerates `data/authorized_keys` with forced commands that call the wrapper. The wrapper does not need Docker socket access.

## 6. Set displayed clone hostname

```bash
export GITMAN_SERVER_HOST=git.example.com
docker compose up -d
```

Users can then clone with:

```bash
git clone git@git.example.com:alice/project.git
```
