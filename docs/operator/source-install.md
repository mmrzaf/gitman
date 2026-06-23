# Install and run from source

## Requirements

- Go `1.26.4`.
- `git` in `PATH` for all Gitman commands after top-level help.
- Docker CLI and Docker daemon access only for `gitman worker`.
- Host OpenSSH only when SSH Git transport is enabled.

The source tree must include the vendored `static/` directory because the web UI assets are embedded into the binary.

## Build

```bash
go test ./...
go build -o bin/gitman ./cmd/gitman
```

## Create the first account

```bash
mkdir -p .data
read -rsp 'Admin password: ' ADMIN_PASSWORD; printf '\n'
printf '%s\n' "$ADMIN_PASSWORD" | ./bin/gitman admin users create admin
unset ADMIN_PASSWORD
```

## Start web

```bash
./bin/gitman web
```

The default port is `8080`. Override it with either `GITMAN_PORT` or:

```bash
./bin/gitman web --port 8081
```

The current web process listens on all interfaces for the selected port. `GITMAN_SERVER_HOST` controls generated SSH clone links; it is not a bind-address setting. Use host firewall rules or a reverse proxy to control exposure.

## Start worker

Run the worker separately only on a host intended to execute CI jobs:

```bash
docker pull golang:1.26-alpine
./bin/gitman worker
```

The worker must run as a numeric non-root UID:GID and requires Docker daemon access. Read [security](security.md) before enabling it.
