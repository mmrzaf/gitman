# Gitman

A self-hosted Git server with built-in pipelines, for one person or a small
team on their own server. Git over HTTPS, a web interface for browsing code
and following runs, and pipelines that build and ship with Docker on the
same host.

It deliberately stays small: one binary, one PostgreSQL database, and a
handful of ideas — people, repositories, ref rules and pipelines.

## How it fits together

- **web** serves Git over HTTP and the web interface. Git's push hooks are
  the same binary, started by Git.
- **worker** claims queued runs, fetches each run's commit from web, and
  runs every step in its own Docker container on the host.
- **PostgreSQL** holds everything except repositories themselves, which
  are bare Git repositories on disk. Web and workers share nothing else,
  so you can run as many workers as you like.

## Running it

You need Docker with Compose, and a Traefik already serving other sites on
the host (the Compose file attaches to its network).

```sh
cp .env.example .env            # then fill it in
sudo install -d -o 1000 -g 1000 /srv/gitman
docker compose up -d --build
```

Create the first admin; the command prints their password:

```sh
docker compose exec web gitman admin person add --admin darius
```

Sign in at `https://<GITMAN_HOST>`, create a repository from Home, and make
an access token under **Access tokens**, in the menu under your username at
the top right of every page. Git uses your username and that token as the
password:

```sh
git clone https://git.example.com/waiotech.git
```

A read token can clone and fetch; pushing needs a write token.

### Behind Traefik

- `GITMAN_TRUSTED_PROXIES` must cover the address Traefik reaches Gitman
  from, or every request appears to come from Traefik and the sign-in
  limiter counts everyone together.
- Large clones and pushes can outlast a proxy's read timeout. If your
  Traefik entry point sets one, raise it, for example
  `--entryPoints.websecure.transport.respondingTimeouts.readTimeout=0`.

### Workers and the data directory

`GITMAN_DATA_DIR` is mounted at the **same path** inside the containers as
on the host. A step container is started by the host's Docker, so the
workspace it mounts must be a host path, and the worker must see it at
that same path. The workspaces directory is private to the worker's
user, and step containers run without the `MKNOD` capability, so a step
cannot leave a device node for the worker to open. Several Gitman
instances can share one Docker host: each labels its step containers
with its own instance ID and cleans up only its own. Run more workers by
scaling the service:

```sh
docker compose up -d --scale worker=3
```

Gitman never pulls images. Every image a pipeline uses (`image`, and
anything listed in `requires`) must already be on the worker's Docker
host; a run with a missing image fails and says which one.

### Mirrors

Every download the image build makes is a build argument (`GO_IMAGE`,
`RUNTIME_IMAGE`, `GOPROXY`, `ALPINE_MIRROR`); see the top of the
`Dockerfile`. Compose's own images are `POSTGRES_IMAGE` and
`GITMAN_IMAGE` in `.env`.

## Configuration

Everything is set through the environment.

| Variable | Default | |
|---|---|---|
| `GITMAN_DATABASE_URL` | — | PostgreSQL connection URL. Required. |
| `GITMAN_DATA_DIR` | `.data` | Repositories, hooks and run workspaces. |
| `GITMAN_PUBLIC_URL` | `http://localhost:8080` | The address people use; shown in clone URLs and push output. |
| `GITMAN_WEB_URL` | the public URL | Where workers reach web to fetch run checkouts. |
| `GITMAN_PORT` | `8080` | The web process's port. |
| `GITMAN_SECRET_KEY` | empty | Encrypts repository secrets; at least 32 characters. Empty disables secrets. Changing it makes stored secrets unreadable. |
| `GITMAN_TRUSTED_PROXIES` | empty | Comma-separated addresses or CIDR ranges allowed to set `X-Forwarded-For`. |
| `GITMAN_RETENTION_DAYS` | `90` | Days finished runs and their logs are kept; `0` keeps them forever. |
| `GITMAN_DATABASE_MAX_CONNS` | `0` | Caps `web` and `worker`'s own connection pool sizes; `0` keeps each process's own default. |
| `GITMAN_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `GITMAN_LOG_FORMAT` | `text` | `text` or `json`. |

See [Configuration reference](docs/operator/configuration.md) for
Compose-level settings and image build arguments.

## People and permissions

- There are no anonymous users: every request, over the web or over Git,
  authenticates as a specific person.
- Anyone can create a repository. An **admin** also manages people and
  repository settings: rules, secrets, read access and deletion. A person
  is disabled, never deleted, so their name stays on what they did.
- **Read access** is per repository: **everyone** signed in (the
  default), or **restricted** to explicit readers and admins. To anyone
  who cannot read a restricted repository, it does not exist — it is left
  out of every list, and Git itself refuses to clone, fetch or push to
  it. Nobody can push to a repository they cannot read.
- **Ref rules** are the only other permission system. A rule matches
  branches or tags by pattern (`main`, `release/*`, `v*`); the most
  specific matching rule applies. It decides:
  - who may push: everyone, admins, or named people;
  - whether force-pushes and deletions are allowed;
  - whether a push runs the pipeline;
  - whether that pipeline may use Docker, get the repository's secrets,
    and ship to a target.

A ref no rule matches is otherwise unprotected — force-push and deletion
are always allowed, and pushes to it run nothing — but who may push to it
follows the repository's **default push policy** (everyone, admins, or
named people; everyone by default); a rule for that specific branch or
tag overrides it. A new repository is fully usable at once; rules and the
default push policy only add protection and grant pipeline capabilities.

Allowing Docker hands a pipeline the host's Docker socket, which is root
on the worker's host: grant it only on refs whose pushers you would give
that. Containers a step starts through the socket are its own; Gitman
stops and removes only the step containers it started.

## Pipelines

A repository's pipeline is `.gitman.yml` at the root of the commit being
run.

```yaml
image: golang:1.27-alpine      # every step runs in this image
docker: false                  # true: steps can use the host's Docker
timeout: 20m                   # the whole run; default 30m, at most 24h
requires:                      # further images the steps use
  - postgres:16-alpine
env:                           # for every step
  CGO_ENABLED: "0"

targets:                       # where a ref ships to
  staging:
    branch: develop
  production:
    tag: "v*"
    env:
      DEPLOY_DIR: /srv/apps/waiotech

steps:
  - name: test
    run: go test ./...
  - name: release
    when: production           # always (default), target, branch, tag, or a target's name
    run: ./deploy.sh
```

- Steps run in order, each in a fresh container with the checked-out
  commit at `/workspace`. The first failing step ends the run; later steps
  are skipped.
- A run's **target** is the one whose pattern matches the ref most
  specifically. A step with `when: target` runs only when some target
  matched; `when: production` only when that one did.
- Every step gets these variables, and the matched target's `env` on top
  of the pipeline's:
  - `GITMAN_REPO`, `GITMAN_RUN`, `GITMAN_COMMIT`, `GITMAN_SHORT`;
  - `GITMAN_REF`, `GITMAN_REF_KIND`, `GITMAN_TARGET`;
  - `GITMAN_VERSION`: the tag name for a tag, otherwise the short commit.
- Secrets are added when the ref's rule allows them. They never appear on
  a command line, and their values are masked in stored output.
- Lines of `key=value` appended to the file `$GITMAN_SUMMARY` appear on
  the run's page.
- A passing run with a target is recorded as a deployment: the
  repository page shows what is live on each target.

There is no built-in artifact or deploy step: a pipeline ships by running
whatever the target actually needs, the same as it would outside Gitman.
A step's container has full outbound network access. Some patterns:

**Deploy over the Docker socket, on the same host as the worker**
(`docker: true`, granted only on the ref that deploys):

```yaml
- name: deploy
  when: production
  run: |
    docker compose -f /srv/apps/waiotech/compose.yaml pull
    docker compose -f /srv/apps/waiotech/compose.yaml up -d
```

**Build and push an image to a registry** (a `REGISTRY_TOKEN` secret,
`--secrets` allowed on the ref):

```yaml
- name: publish
  when: production
  run: |
    echo "$REGISTRY_TOKEN" | docker login registry.example.com -u deploy --password-stdin
    docker build -t registry.example.com/waiotech:"$GITMAN_VERSION" .
    docker push registry.example.com/waiotech:"$GITMAN_VERSION"
```

**Deploy over SSH**, with the private key and `known_hosts` stored as
secrets (a secret's value may be multi-line, such as a whole key file):

```yaml
- name: deploy
  when: production
  run: |
    install -m 600 -D /dev/stdin ~/.ssh/id_ed25519 <<< "$DEPLOY_KEY"
    install -m 644 -D /dev/stdin ~/.ssh/known_hosts <<< "$KNOWN_HOSTS"
    ssh deploy@app.example.com "cd /srv/apps/waiotech && git pull && ./restart.sh"
```

**Upload a build output with curl**:

```yaml
- name: upload
  run: |
    go build -o bin/waiotech .
    curl -fsS -H "Authorization: Bearer $UPLOAD_TOKEN" -T bin/waiotech \
      https://artifacts.example.com/waiotech/"$GITMAN_VERSION"
```

Check a pipeline file before pushing it:

```sh
gitman check .gitman.yml
```

A push prints each run it started, with a link. Runs can also be started
by hand — **Run again** on a run, **Run this commit** on a commit — by
anyone allowed to push to the ref, and cancelled by the same people from
the run's page, or with `gitman admin run cancel <repo> <number>`. A run
of a bare commit has no ref, so any member may start or cancel it.

## The command line

The same binary administers an instance
(`docker compose exec web gitman admin ...`):

```
gitman admin person add [--admin] <username>
gitman admin person list | disable | enable | reset-password <username>
gitman admin person role <username> admin|member
gitman admin token create [--write] [--days N] <username> <name>
gitman admin repo create [--description TEXT] [--default-branch NAME] <name>
gitman admin repo list | delete <name> | sync <name>
gitman admin rule list <repo>
gitman admin rule set [--push everyone|admins|people] [--people a,b] [--force] [--delete]
                      [--run] [--docker] [--secrets] [--ship] <repo> branch|tag <pattern>
gitman admin rule delete <repo> branch|tag <pattern>
gitman admin run cancel <repo> <number>
gitman admin worker cleanup
gitman admin migrate
```

## Operating it

- **Backups:** the database (`pg_dump`) and the `repos` directory under
  `GITMAN_DATA_DIR`. Hooks are rewritten at every start, and workspaces
  exist only while a run does. After restoring repositories from a
  different moment than the database, run `gitman admin repo sync <name>`
  to rebuild each one's ref index from Git.
- **Retention:** the web process prunes expired sessions and finished
  runs older than `GITMAN_RETENTION_DAYS` every hour. Each ref's latest
  run and every deployment record are kept.
- **Upgrades:** migrations run automatically when web or a worker starts.
- **Lost workers:** a run whose worker stops responding for a minute is
  failed with that reason, and its step container and workspace are
  removed by a live worker on the same host within another few minutes.
  After a database outage, a worker waits a minute of its own healthy
  heartbeats before judging any other worker lost. A worker whose Docker
  daemon does not answer claims no runs until it does.
  If a worker is killed outright (SIGKILL, OOM) and nothing else is
  running on that host, nothing removes them automatically; run
  `gitman admin worker cleanup` there, or schedule it, to reclaim them.
  Runs are never retried automatically.
- **Health:** `/healthz` reports the process is up; `/readyz` also checks
  the database.

## Developing

Go 1.27 and Git.

```sh
go build ./...
go test ./...        # tests that need PostgreSQL skip themselves
```

To run them all:

```sh
GITMAN_TEST_DATABASE_URL='postgres://postgres@localhost/gitman_test?sslmode=disable' go test -p 1 ./...
```

## Not added, on purpose

- **SSH.** There is no SSH transport at all, now or planned. Git over
  HTTPS with access tokens is the only way in, secured one way.
- **Pull requests, issues, wikis, forks, stars.** Gitman is where code
  lives and ships from; discussion lives elsewhere.
- **Organizations, namespaces.** One flat list of repositories.
- **Anonymous or public access, of any kind.** Every request, over the
  web or over Git, authenticates as a specific person; there is no
  read-only or unauthenticated mode, and no way to turn one on.
- **Pulling images.** What a pipeline runs is what the operator put on
  the host.
- **Automatic retries, caches between runs, artifacts, matrix builds,
  scheduled runs.** A run is one commit, run once, in order.
- **Webhooks, email and plugins.**
- **Deleting people.** People are disabled, so history keeps their names.
- **SQLite or other databases.** PostgreSQL's notifications and row
  locking are what let web and workers coordinate without talking to
  each other.
- **Syntax highlighting.** Code is shown as it is.
