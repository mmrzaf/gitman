# Security model

## People

- There are no anonymous users: every request, over the web or over Git,
  authenticates as a specific person.
- Anyone signed in can create up to 100 repositories and 10 per hour.
- Raw file downloads from the web are capped at 1 GiB; use a Git clone for
  larger files.
- An **admin** additionally manages people (add, disable, enable, change
  role) and every repository's settings: description, ref rules, secrets,
  read access, and deletion. A person is disabled, never deleted, so
  their name stays on what they did.
- Authentication is a username plus either a password (web sign-in) or an
  access token (Git over HTTPS, and any script using the token). There is
  no SSH transport.

## Credentials

Generated passwords for a new account, reset, or re-enabled account expire
in 24 hours. The person must change that password before using the rest of
the interface or creating tokens. Passwords require at least 12 characters
and at most 72 UTF-8 bytes. Usernames and repository names are lowercase.

Sessions have an absolute seven-day expiry. A person keeps at most 20
sessions and 50 access tokens; creating more removes the oldest. Stored user
agents are truncated to 512 bytes.
 Password changes, resets, and
disabling an account revoke sessions and access tokens. **Revoke all credentials**
on the account page, or `gitman admin person revoke-all <username>`, also ends
every session and token while preserving the password. Tokens cover all current and future repositories by default, or selected
repositories, with a read/write scope. They default to 30 days and last
at most 365 days. Repository permissions still apply on every Git request.
Live streams recheck credentials and permissions before each batch and
close after revocation.

The account page lists every signed-in browser by an opaque session ID, with its
address, user agent, last-seen time and expiry; a session can be revoked there
without exposing its cookie. Admins see all sessions under **Sessions** and can
revoke anyone's; that action is recorded in Activity. The session cookie's token
hash is never shown. The same opaque handles and last-used times are available
with `gitman admin session list` / `revoke` and `gitman admin token list`.

Sign-in is limited per username and address, and per username across
addresses, so parallel guesses cannot pass the check before bcrypt finishes.
IPv6 clients are counted by their /64. Wrong current-password checks on the
account page are limited the same way. A generated password is shown once in
the response that creates it; refreshing that page re-submits the action, so
copy the password before leaving it.

## Repository read access

Each repository has one visibility, set on its Settings page's **Access**
tab or with `gitman admin repo visibility`:

- **Everyone** (the default) — any signed-in person can read it.
- **Restricted** — only its explicit readers (`gitman admin reader
  add`/`remove`/`list`) and admins can read it.

To anyone who cannot read a restricted repository, it does not exist: it
is left out of Home, the go-to-file finder, activity, live updates and
every other list, and every route that names it — including Git's own
clone, fetch and push — answers as if no repository by that name had ever
existed, never with a distinguishable "forbidden." Nobody can push to a
repository they cannot read, regardless of what its ref rules allow.
Deleting a repository is an admin-only event; its name is never announced to
members who could not read it. Person-level events (added, disabled, role
changed, password reset, credentials revoked, session revoked) are admin-only
for the same reason.

Repository names are globally unique, so creating one that already exists
answers "already exists" even when the existing repository is restricted and
the person cannot read it. That is a deliberate exception: hiding the name
would require per-person aliases or a different uniqueness rule, and the answer
reveals only that some repository uses that name, never its contents, owner or
activity.

## Ref rules

Ref rules are the only other permission system, and the only thing that
gates a pipeline's privileges. A rule matches branches or tags by pattern
(`main`, `release/*`, `v*`); when more than one rule could match a given
ref, the most specific pattern wins. A rule decides:

- **who may push** — everyone, admins only, or a named list of people
  (`--push`, `--people`);
- **whether force-push and deletion are allowed** (`--force`, `--delete`);
- **whether a push runs the pipeline** (`--run`);
- **whether a triggered run may use Docker** (`--docker`), **receive the
  repository's secrets** (`--secrets`), and **execute a managed deploy step for a target** (`--deploy`).

Managed deployment permission controls target serialization and deployment
history. It does not prohibit external changes in other steps. Network access,
credentials or Docker access can let any script deploy; trust the people who
can change and execute those scripts.

A ref that no rule matches is otherwise unprotected — force-push and
deletion are always allowed on it, and pushes to it trigger nothing — but
who may push to it follows the repository's **default push policy**
(everyone, admins, or a named list of people; everyone by default), set
next to its ref rules. A rule for that specific branch or tag overrides
the default; force-push and deletion on an unmatched ref are unaffected
either way. A brand new repository is fully usable immediately — rules
and the default push policy only add restriction and grant pipeline
capabilities, they're never required to use a repository at all.

Set rules and the default push policy with `gitman admin rule set` /
`gitman admin repo default-push` (see the
[CLI reference](../reference/cli.md)) or from a repository's Settings
page.

## Docker access is the sensitive one

Allowing Docker (`--docker`) hands a pipeline the host's Docker socket,
which is effectively root on the worker's host. Grant it only on refs
whose pushers you'd trust with root on that machine — for example `main`
restricted to admins, not every branch. Containers a step starts through
the socket belong to that step; Gitman only stops and removes the step
containers it started itself, never anything else on the host.

The worker process itself runs as `root` in the supported Compose setup.
Step containers run as whatever user their own image specifies, often
root, and Gitman does not force a non-root user inside them; the worker
must be able to remove whatever files they leave in a run's workspace.
Holding the Docker socket makes the worker root-equivalent on the host
either way. This is a property of how the worker is deployed, not
something a ref rule changes.

By default, step containers have 2 GiB total memory (including swap), two CPUs,
and 256 PIDs available. The operator can configure these limits; containers
never pull images automatically. These limits apply to the step container;
a Docker socket grant can create other containers outside them.

A step's container has full outbound network access, the same as any
other container on the host's Docker network: nothing in Gitman isolates
a pipeline's network access per ref or per repository. This is what lets
a step push an image to a registry, deploy over SSH, or upload a build
artifact — see [Pipelines](../../README.md#pipelines) for examples — and
it is also why `--docker` and, to a lesser extent, unrestricted outbound
access are only for refs whose pushers are trusted.

`GITMAN_STEP_MEMORY_MIB`, `GITMAN_STEP_CPUS` and `GITMAN_STEP_PIDS` bound the
step container Gitman starts. They do not bound a `docker build` or any other
container a socket-enabled step creates: build daemons and child containers run
outside those limits. Bound those at the Docker level (daemon defaults or build
resource limits) if a pipeline builds on the worker.

## Secrets

See [Secrets](../ci/secrets.md) — encrypted with `GITMAN_SECRET_KEY`,
gated per-ref by `--secrets`, masked in stored run output, and never
placed on a command line. Masking is exact-byte matching after terminal
escapes are removed: it catches a secret split across writes or wrapped in
colour codes, but it cannot catch every encoded form (base64, compression,
character-by-character printing). Anyone who can change and run a
secret-enabled ref can make the pipeline print the secret; the ref rule is the
permission boundary, not log masking.

## Cross-site request forgery

Every state-changing web request (anything but GET/HEAD/OPTIONS) is checked
against where it came from: the browser's `Sec-Fetch-Site` header, or
failing that its `Origin` header, must say the request is same-origin. A
cross-site request — from another site's form or script — is refused
before it reaches a handler. A request carrying neither header does not
come from a browser, so there is no forgery to prevent; this, together
with the session cookie's `SameSite=Lax`, is the whole defense — there
are no separate per-form CSRF tokens to keep in sync. Git's smart HTTP
POST routes authenticate with HTTP Basic access tokens instead of a session
cookie, so they are outside this check; a token is not sent by a browser
automatically and an attacker cannot make a forged request carry it.

## Cookies and proxies

The session cookie's `Secure` attribute follows `GITMAN_PUBLIC_URL`'s
scheme automatically. If Gitman is behind a reverse proxy, set
`GITMAN_TRUSTED_PROXIES` to the proxy's actual reachable address —
otherwise the sign-in rate limiter (and anything else keyed on client IP)
treats every proxied client as the same one. With no trusted proxies, one
person's failed attempts can exhaust the shared address budget and lock out
sign-in for everyone behind that proxy for fifteen minutes. Gitman warns at
startup the first time a request arrives from a private address in that state.
The production Compose example defaults the trusted range to `172.16.0.0/12`;
override it if the Compose network uses another range.

Gitman sets `Strict-Transport-Security` when `GITMAN_PUBLIC_URL` is HTTPS. If a
TLS-terminating proxy is in front, or Gitman itself speaks plain HTTP behind
one, configure HSTS at the proxy as well; a header the client never sees over
plain HTTP cannot protect the first request.

## Resource bounds

Git and archive requests share a concurrency limit; four slots are reserved for
a worker fetching a run's commit, so a burst of clones cannot make a run fail
with a 503. Raw file downloads are streamed without taking a Git slot and are
bounded by the file size Git already serves. Repository Overview and Home read
the indexed refs rather than walking Git, but a repository with an extreme
number of refs still costs more to render; the page deadline bounds the damage
rather than paginating refs in this release.

## Execution and storage limits

A claim persists one deadline before preparation begins. It includes secret
loading, Git checkout, image checks, and all steps. Worker database operations
and Docker probes have 10-second budgets; cleanup and final recording have
30-second budgets. A failed cleanup retains the workspace and pauses claims
until recovery confirms termination. Gitman does not automatically rerun scripts.

Each run has a monitored workspace budget (10 GiB by default). Checks run every five seconds,
including during checkout. Workers stop claiming below the configured disk
reserve (5 GiB by default). See [Configuration](configuration.md).
These are monitored limits: writes can overshoot between checks. Image storage
is bounded separately by the image and build-cache management described in
[Docker deployment](docker.md#disk-use-and-images); socket-created child
containers are still the pipeline's responsibility.

Workers advertise current image inventories. Runs stay queued when no worker
has every required image. The admin **Workers** page distinguishes readiness
from a heartbeat and shows readiness failures and available images; **Images**
shows the full classified inventory and the build-cache policy. Image removal
and cache pruning are admin actions executed by the worker that owns the host,
never by the web process.
