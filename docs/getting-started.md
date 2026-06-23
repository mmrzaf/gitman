# Getting started

The fastest supported setup is Docker Compose on a Linux Docker host.

## 1. Choose web-only or web plus CI

A web-only server provides the browser UI and Git over HTTP:

```bash
export GIT_UID=$(id -u)
export GITMAN_DATA_DIR="$(pwd)/data"
mkdir -p "$GITMAN_DATA_DIR"
chmod 700 "$GITMAN_DATA_DIR"
docker compose up -d --build web
```

To run the built-in CI worker too, grant the worker access to the Docker socket and start both services:

```bash
export GIT_UID=$(id -u)
export DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
export GITMAN_DATA_DIR="$(pwd)/data"
mkdir -p "$GITMAN_DATA_DIR"
chmod 700 "$GITMAN_DATA_DIR"
docker compose up -d --build
```

The default published address is `127.0.0.1:8080`.

## 2. Create the first account

Passwords are read from standard input so they do not appear in shell history or process listings.

```bash
read -rsp 'Admin password: ' ADMIN_PASSWORD; printf '\n'
printf '%s\n' "$ADMIN_PASSWORD" | docker compose exec -T web gitman admin users create admin
unset ADMIN_PASSWORD
```

Usernames must be 3 to 32 characters and contain only letters, numbers, dashes, and underscores. Passwords must be at least 8 characters and contain at least one letter and one digit.

## 3. Sign in and create a repository

Open `http://localhost:8080`, sign in, open **Repositories**, and create a repository. Repositories can be public or private.

## 4. Push over HTTP

Create a personal access token from **Access Tokens** for routine Git-over-HTTP use. The token is displayed once.

```bash
git remote add origin http://localhost:8080/admin/example.git
git branch -M main
git push -u origin main
```

When Git prompts for credentials, use your Gitman username and a personal access token as the password. Account passwords are not accepted for Git Smart HTTP.

## 5. Enable CI only when needed

Pre-pull each approved job image on the Docker host. Gitman will not pull images during a run.

```bash
docker pull debian:bookworm-slim
```

Add a root-level `.gitman-ci.yml` to a repository:

```yaml
image: debian:bookworm-slim
steps:
  - name: verify
    run: echo "CI is working"
```

Push the file, open the repository's **CI/CD** page, and select **Run CI**. Repository owners can install the post-receive auto-trigger hook from the same page.

## 6. Before exposing Gitman

Configure HTTPS, set `GITMAN_PUBLIC_URL`, enable secure cookies, and read the [security guide](operator/security.md). SSH transport is separate and optional; see [SSH setup](operator/ssh.md).
