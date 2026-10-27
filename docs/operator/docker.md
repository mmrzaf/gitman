# Docker deployment

The standalone Compose configuration includes PostgreSQL, web, a worker,
and initialization of the data directory. It needs no external Docker
networks or pre-existing reverse proxy. Copy `.env.example` to `.env`, set
a database password and an absolute host data directory, then run:

```sh
docker compose up --build -d
docker compose exec web gitman admin person add --admin darius
```

Web listens on `127.0.0.1:8080` by default. PostgreSQL is published only on
loopback, at configurable `GITMAN_DATABASE_PORT` (default 5433). The web
health check uses readiness; workers wait for web and database readiness.

For public HTTPS, set `GITMAN_DOMAIN` to a domain pointing to this server:

```sh
docker compose -f compose.yaml -f compose.production.yaml up --build -d
```

The production example adds Caddy on ports 80 and 443. Its certificate and
configuration storage persist in named volumes. Keep the app's published
port bound to loopback. When you configure trusted proxies for client-IP
forwarding, use the actual proxy addresses or network; do not trust all
addresses.

Workers fetch from `http://web:8080`. Service names stay within the Compose
project; there are no fixed container names or external network names.
Scale workers with `docker compose up -d --scale worker=3`.

Every worker controls the host Docker socket and is root-equivalent on that
host. Pipeline images and required images must already be present there;
Gitman never pulls them. Ordinary step limits apply to the step container,
not containers a socket-enabled step creates. The data path is mounted at
the identical absolute path inside and outside the worker because step
containers bind-mount workspaces by host path.

The worker gets an explicit environment rather than the whole `.env`, so the
database password variable is not handed to a root process that also holds the
Docker socket. The database publishes only on loopback (`127.0.0.1:5433` by
default) for host-side `pg_dump`/`psql`; web and worker use the Compose network.
Compose applies log rotation, memory limits (`GITMAN_WEB_MEMORY_LIMIT`,
`GITMAN_WORKER_MEMORY_LIMIT`, `GITMAN_DATABASE_MEMORY_LIMIT`,
`GITMAN_PROXY_MEMORY_LIMIT`) and health checks to the web and proxy. The worker
has no HTTP endpoint: its liveness is its process, and Compose restarts it if
that exits. A worker that stops heartbeating has its runs failed after five
minutes even if the container itself is gone.

## Disk use and images

Every pipeline run executes in images that must already exist on the worker
host; Gitman still never pulls an image. A pipeline may also declare the images
it builds under `images:` in `.gitman.yml`. Those declarations are how Gitman
knows which `repo:tag` images are its own and how many of each to keep.

Each worker publishes its host's inventory to PostgreSQL: every tagged image,
every dangling image, the containers that use each image, and the build cache's
size and reclaimable size. The admin **Images** page and
`gitman admin images list` show that inventory per Docker host (workers sharing
one host share one inventory). An inventory older than five minutes is marked
stale, and no prune or removal acts on it.

Each image is classified as:

- **Required** — it is the `image` or a member of `requires` of a pipeline that
  can still run: every queued or running run, and the latest stored pipeline of
  every existing branch or tag matched by a ref rule. An exact required
  reference (including implicit `:latest` and digest references) is never
  pruned and cannot be removed from the page or by `images remove`.
- **Built** — its repository appears in a pipeline's `images:`. The image also
  reports its keep count, whether it is the deployed version, and whether it is
  within the newest `keep` tags. Tags are ordered by Docker's image creation
  time; ties are deterministic.
- **Other** — everything else, including dangling images. Automatic pruning
  never touches these. Only an explicit removal removes one.

In-use images (a running or stopped container references the image ID) are
protected even when they are built, and can only be removed after the container
is gone. The currently deployed version comes from the latest successful
deployment per target of the pipeline's repository, mapped to
`<repository>:<version>`. If a recorded version cannot be matched to a live tag,
Gitman protects the newest tag rather than risk removing the live image.

Image automation is **off by default**. No image is removed automatically
unless an operator enables `GITMAN_IMAGE_AUTOMATION`:

- `off` (the default) — only manual pruning and removal.
- `after-deploy` — after a passed run with a deploy step on that worker, prune
  built images beyond their keep count.
- `low-disk` — when free space on the worker's data filesystem falls below
  `GITMAN_DISK_RESERVE_GIB` plus 2 GiB, prune built images beyond their keep
  count. It acts at most once an hour. The worker can only measure the
  filesystem holding its workspace root; on the supported Compose layout that is
  the same data disk as Docker's image and cache storage. If you put Docker's
  data root on another filesystem, monitor that filesystem separately.

Manual work:

```sh
gitman admin images list [--worker ID] [--class required|built|other] [--json]
gitman admin images missing
gitman admin images prune [--worker ID] [--confirm]
gitman admin images prune --cache [--worker ID] [--confirm] [--keep-storage GiB] [--older-than HOURS]
gitman admin images remove [--confirm] <worker-id> <image>
```

`prune` and `remove` are dry runs without `--confirm`. Confirmed work is queued
as a durable operation for the owning worker and appears on **Operations** and
the **Images** page; the worker re-reads the inventory and the required set
before acting, and skips anything that became required, in use or deployed. A
removal uses `docker image rm` without `--force`, so it removes a tag rather
than an image another tag still uses, and it refuses an image a container uses.
`images missing` exits non-zero when a ready worker lacks a required image.

Two workers on one Docker host share the inventory and the operation queue.
Only one can hold the host's image-execution lock, so removals cannot overlap a
run or each other. Enabling maintenance stops workers from claiming new image
operations; an operation already running finishes or is recorded as
interrupted. Builds and removals never run concurrently on one host.

## Build cache

The Docker build cache is rebuildable data, and an unbounded cache is the most
common way a Gitman worker host fills its disk. Gitman therefore manages it by
default, separately from image automation.

- **Policy:** keep at most **3 GiB** and no cache entry older than **7 days**.
  These defaults suit a small host: they leave room for the disk reserve,
  workspaces, PostgreSQL and a build that temporarily exceeds the cache cap.
- **Enforcement:** on by default. A worker enforces the policy after every run
  that used Docker (once its containers are gone) and every 15 minutes. It uses
  `docker builder prune --force --all --keep-storage <cap> --filter until=<age>`
  and, if recent unused cache still leaves the cache above the cap, a second
  pass with the cap alone. `--all` is required: without it Docker removes only
  dangling cache and leaves the layer cache of build steps, so the cap could
  never be met. Records a running build holds are never removed. Enforcement
  never touches images, containers or volumes. Enforcement is serialized with runs by the host lock,
  so it never races a build.
- **A ceiling after enforcement:** the cache can exceed the cap while a build is
  using it. The cap is what enforcement leaves behind, not a limit during a
  build; size `GITMAN_DISK_RESERVE_GIB` with that headroom in mind.
- **Tune or disable:** `GITMAN_IMAGE_CACHE_KEEP_GIB`,
  `GITMAN_IMAGE_CACHE_MAX_AGE_HOURS` and `GITMAN_IMAGE_CACHE_ENABLED`. Turning
  enforcement off is an explicit choice, not the default, and leaves the cache
  to the daemon and the operator.
- **See what it did:** the **Images** page and `images list` show cache size,
  reclaimable size, the policy, when enforcement last ran, what it reclaimed and
  any error. A cache still above its cap is shown as a warning, and a failure is
  recorded in Activity. A failed prune (daemon unreachable, a build holding
  cache) does not fail the run; the next trigger retries.
- **Assumption:** on a Gitman worker host, every image build comes from Gitman
  pipelines, so Gitman may treat the host's whole BuildKit cache as its own. A
  daemon shared with other build tools will have its cache pruned too; disable
  enforcement or give those tools a separate daemon.

Manual enforcement uses the same code path:

```sh
gitman admin images prune --cache
gitman admin images prune --cache --confirm
gitman admin images prune --cache --confirm --keep-storage 5 --older-than 24
```

The page's **Prune cache** action applies the worker's current policy.

### Disk is filling up

1. Open **Images** (or `gitman admin images list`) and find the host.
2. Check the build cache first. If it is above its cap or has a recorded error,
   run `gitman admin images prune --cache --confirm`.
3. Review prunable built images and run `gitman admin images prune --confirm`.
   Start with one host if you have several.
4. If the pressure repeats, enable `GITMAN_IMAGE_AUTOMATION=after-deploy` or
   `low-disk` for the worker, and lower `GITMAN_IMAGE_CACHE_KEEP_GIB` if the
   host is small.
5. Anything left is an "other" image from outside Gitman's declarations. Remove
   one explicitly with `gitman admin images remove --confirm <worker-id> <image>`
   only after checking what uses it. Never remove a required image.

A run stuck in the queue because an image is missing is a different problem:
`gitman admin images missing` names the image and the pipelines that require it,
and the **Images** page shows the same under **Missing required images**. Make
the image present on that worker's Docker host — pull or build it there — and
the worker advertises it on its next readiness check. Gitman will not pull it.

The Gitman image does not include PostgreSQL client tools. `admin maintenance
backup` and `gitman restore` therefore run on a host with a `pg_dump`/
`pg_restore` matching the database major version; see
[Backups and upgrades](backups-and-upgrades.md).

## Reverse proxy timeouts

Clones, pushes and archive downloads stream for as long as the transfer takes,
and a Git HTTP request may run for an hour. If a reverse proxy in front of
Gitman has a short read or write timeout, a large transfer is cut off even
though Gitman is healthy. Raise the proxy's timeouts for the Git routes, or
disable request buffering and let Gitman stream. Caddy's defaults are generous;
an nginx `proxy_read_timeout` or `proxy_send_timeout` of a few minutes is not.

Use an empty database and data directory for a new installation. See
[Configuration](configuration.md) and [Backups and recovery](backups-and-upgrades.md).
