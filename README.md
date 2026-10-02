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

The standalone Compose setup includes PostgreSQL and requires no external
networks or reverse proxy. Copy `.env.example` to `.env`, set a database
password and an absolute data directory, then start it:

```sh
cp .env.example .env
docker compose up --build -d
```

Web is available at `http://localhost:8080`. For public HTTPS, configure
`GITMAN_DOMAIN` and use the [production Compose example](docs/operator/docker.md).
The schema has one current baseline and ordinary versioned migrations.

Create the first admin; the command prints their password:

```sh
docker compose exec web gitman admin person add --admin darius
```

Sign in at `http://localhost:8080` (or your configured HTTPS address), change
the temporary password, create a repository from Home, and
make an access token under **Access tokens**, in the menu under your
username at the top right of every page. Git uses your username and that
token as the password:

```sh
git clone https://git.example.com/waiotech.git
```

A read token can clone and fetch; pushing needs a write token. Tokens cover all
current and future repositories by default; optionally limit
one to selected repositories. Your permissions still apply. Tokens expire after
30 days by default.
[Docker deployment](docs/operator/docker.md) has the whole setup.

### Behind a reverse proxy

- `GITMAN_TRUSTED_PROXIES` must cover every proxy in front of Gitman:
  the reverse proxy, and a CDN if one is in front of it. Otherwise
  every request appears to come from the nearest proxy and the sign-in
  limiter counts everyone together.
- Clones and pushes stream, and can be large and slow. If the entry point
  sets a short transfer timeout, configure it to accommodate those transfers.

### Workers and the data directory

`GITMAN_DATA_DIR` is mounted at the **same path** inside the containers as
on the host. A step container is started by the host's Docker, so the
workspace it mounts must be a host path, and the worker must see it at
that same path. The workspaces directory is private to the worker's
user, and step containers run without the `MKNOD` capability, so a step
cannot leave a device node for the worker to open. Several Gitman
instances can share one Docker host: each labels its step containers
with its own instance ID and cleans up only its own.

Gitman never pulls images. Every image a pipeline uses (`image`, and
anything listed in `requires`) must already be on the worker's Docker
host. A run stays queued until a ready worker has every required image.
Execution checks again before starting a step.

### Mirrors

Every download the image build makes is a build argument (`GO_IMAGE`,
`RUNTIME_IMAGE`, `DOCKER_CLI_IMAGE`, `POSTGRES_IMAGE`, `DEBIAN_MIRROR`,
`DEBIAN_SECURITY_MIRROR`, `GOPROXY`); see the top of the `Dockerfile`.
Gitman's own [`.gitman.yml`](.gitman.yml) uses the public registries and module
proxy by default; change these arguments to use your own mirrors.

## Configuration

Everything is set through the environment.

| Variable | Default | |
|---|---|---|
| `GITMAN_DATABASE_URL` | — | PostgreSQL `postgres://` or `postgresql://` connection URL. Required. |
| `GITMAN_DATA_DIR` | `.data` | Repositories, hooks and run workspaces. |
| `GITMAN_PUBLIC_URL` | `http://localhost:8080` | The address people use; shown in clone URLs and push output. |
| `GITMAN_WEB_URL` | the public URL | Where workers reach web to fetch run checkouts. |
| `GITMAN_PORT` | `8080` | The web process's port. |
| `GITMAN_SECRET_KEY` | empty | Encrypts repository secrets; base64 encoding of exactly 32 random bytes. Empty disables secrets. Changing it makes stored secrets unreadable. |
| `GITMAN_TRUSTED_PROXIES` | empty | Comma-separated addresses or CIDR ranges allowed to set `X-Forwarded-For`. |
| `GITMAN_LOG_RETENTION_DAYS` | `30` | Log retention, independent from run summaries. |
| `GITMAN_RUN_RETENTION_DAYS` | `90` | Run metadata; retains latest indexed current-head summary. |
| `GITMAN_AUDIT_RETENTION_DAYS` | `365` | Push, refusal, settings and completed-operation history. |
| `GITMAN_DEPLOYMENT_RETENTION_DAYS` | `365` | Deployment history; retains latest record per target. |
| `GITMAN_DATABASE_MAX_CONNS` | `0` | Caps `web` and `worker`'s own connection pool sizes; `0` keeps each process's own default. |
| `GITMAN_STEP_MEMORY_MIB` | `2048` | Memory including swap per step container, in MiB. |
| `GITMAN_STEP_CPUS` | `2` | CPU quota per step container. |
| `GITMAN_STEP_PIDS` | `256` | Maximum processes per step container. |
| `GITMAN_WORKSPACE_GIB` | `10` | Monitored workspace size per run, in GiB. |
| `GITMAN_DISK_RESERVE_GIB` | `5` | Minimum free space for pushes and worker execution, in GiB. |
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
    and execute an explicit deploy step.

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
image: docker:29-cli           # every step runs in this image
docker: true                   # steps use the host's Docker; a ref rule must allow it
timeout: 20m                   # the whole run; default 30m, at most 24h
requires:                      # images that must already be on the host
  - golang:1.27-bookworm
  - debian:bookworm-slim
env:                           # for every step
  GOPROXY: https://goproxy.example.com,direct

targets:                       # target contexts matched by ref
  staging:
    branch: develop
    env:
      DEPLOY_DIR: /srv/apps/waiotech-stage
  production:
    tag: "v*"
    env:
      DEPLOY_DIR: /srv/apps/waiotech

steps:
  - name: build
    run: docker build --pull=false --build-arg GOPROXY="$GOPROXY" -t "waiotech:$GITMAN_VERSION" .
  - name: check
    run: docker run --rm "waiotech:$GITMAN_VERSION" waiotech version
  - name: deploy
    type: deploy
    when: target               # always (default), target, branch, tag, or a target's name
    run: ./deploy.sh "$DEPLOY_DIR" "waiotech:$GITMAN_VERSION"
```

A pipeline is meant to stay this light: build, check, and deploy. Each
step is one container; Gitman starts no databases or other services
next to it, so tests that need them belong in a CI that has them.

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
  - `GITMAN_VERSION`: for a tag, its name made safe as an image tag; for
    a branch, the commit's first 12 characters.
- Secrets are added when the ref's rule allows them. They never appear on
  a command line, and their values are masked in stored output.
- Lines of `key=value` appended to the file `$GITMAN_SUMMARY` appear on
  the run's page.
- A successful `type: deploy` step records a deployment. Later checks may
  fail without erasing that deployment receipt.

There is no artifact service. A `type: deploy` step records a successful
deployment script; it does not verify service health. Pipelines deploy by running
whatever the target actually needs, the same as it would outside Gitman.
A step's container has full outbound network access. Some patterns:

**Deploy over the Docker socket, on the same host as the worker**
(`docker: true`, granted only on the ref that deploys). A step's own
filesystem is only the checkout, so it reaches a host directory through a
container it starts on the host's Docker:

```yaml
- name: deploy
  type: deploy
  when: production
  run: |
    docker run --rm \
      --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
      --mount type=bind,src=/srv/apps/waiotech,dst=/srv/apps/waiotech \
      --workdir /srv/apps/waiotech \
      docker:29-cli sh -ec "
        sed -i 's#^APP_IMAGE=.*#APP_IMAGE=waiotech:$GITMAN_VERSION#' .env
        docker compose up -d"
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
  type: deploy
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

A push prints each run it started, with a link. Every run is of a
branch or tag. Runs can also be started by hand — **Run** on the Runs
page or a branch's row, and **Run again** on a run — by anyone allowed
to push to the ref, and cancelled by the same people from the run's
page, or with `gitman admin run cancel <repo> <number>`.

## The command line

The same binary administers an instance
(`docker compose exec web gitman admin ...`):

```
gitman admin person add [--admin] <username>
gitman admin person list | disable | enable | reset-password <username>
gitman admin person role <username> admin|member
gitman admin token create [--write] [--days N] [--repos name[,name]] <username> <name>
gitman admin repo create [--description TEXT] [--default-branch NAME] <name>
gitman admin repo list | delete <name> | sync <name>
gitman admin repo visibility <name> everyone|restricted
gitman admin repo default-branch <name> <branch>
gitman admin repo default-push [--push everyone|admins|people] [--people a,b] <name>
gitman admin reader add | remove <repo> <username>
gitman admin reader list <repo>
gitman admin rule list <repo>
gitman admin rule set [--push everyone|admins|people] [--people a,b] [--force] [--delete]
                      [--run] [--docker] [--secrets] [--deploy] <repo> branch|tag <pattern>
gitman admin rule delete <repo> branch|tag <pattern>
gitman admin run cancel <repo> <number>
gitman admin worker cleanup
gitman admin migrate
```

## Operating it

Use maintenance snapshots to keep PostgreSQL and Git state consistent. Back up
with `gitman admin maintenance enable`, wait for `maintenance status`, then
`maintenance backup <new-directory>`. Store the encryption key separately. Restore
requires an empty database and data directory and leaves maintenance enabled.
See [backups and recovery](docs/operator/backups-and-upgrades.md).

Workers preserve execution receipts until recording commits. Recovery confirms
termination before releasing deployment ownership, and never reruns scripts.
Pending repository operations are visible in **Operations**; worker readiness and
local image inventory appear in **Workers**. Logs have independent age and size
limits. See [architecture](docs/architecture.md) and [configuration](docs/operator/configuration.md).

The schema has one current baseline and ordinary versioned migrations.

## Developing

Go 1.27 and Git.

```sh
go build ./...
go test ./...        # tests that need PostgreSQL skip themselves
```

To run them all:

```sh
GITMAN_TEST_DATABASE_URL='postgres://postgres@localhost/gitman_test?sslmode=disable' go test ./...
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

## License

Gitman is available under the [MIT license](LICENSE). Dependency licenses
and notices are included in [Third-party notices](THIRD_PARTY_NOTICES.md).
