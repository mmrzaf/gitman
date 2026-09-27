# Building from source

Docker Compose is the supported way to run Gitman (see
[Docker deployment](docker.md)), but the binary has no hard dependency on
containers beyond what pipelines themselves need — running `web` and
`worker` from a plain binary is possible if you provide PostgreSQL and,
for `worker`, a reachable Docker daemon yourself.

## Build

```sh
go build -trimpath -ldflags "-s -w -X main.version=1.0.0" -o gitman ./cmd/gitman
```

Or with `make`:

```sh
make build            # bin/gitman, VERSION=dev
make build VERSION=1.0.0
make install          # installs to /usr/local/bin
```

Requires Go 1.27.

## Running

```sh
export GITMAN_DATABASE_URL='postgres://gitman:password@localhost:5432/gitman?sslmode=disable'
export GITMAN_DATA_DIR=/srv/gitman
export GITMAN_PUBLIC_URL=https://git.example.com

./gitman web
./gitman worker      # elsewhere, or as a second process; needs Docker
```

Migrations run automatically on start; there's no separate migrate step
required (`gitman admin migrate` exists for running them by hand if
needed).

If `worker` runs outside of Compose, it needs the same view of
`GITMAN_DATA_DIR` that `web` has, and a Docker daemon it can reach — see
[Workers and the data directory](docker.md#the-data-directory) for why
the path has to match exactly.

See [Configuration reference](configuration.md) for every setting.
