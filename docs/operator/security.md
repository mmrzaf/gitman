# Security model

## People

- There are no anonymous users: every request, over the web or over Git,
  authenticates as a specific person.
- Anyone signed in can create a repository.
- An **admin** additionally manages people (add, disable, enable, change
  role) and every repository's settings: description, ref rules, secrets,
  read access, and deletion. A person is disabled, never deleted, so
  their name stays on what they did.
- Authentication is a username plus either a password (web sign-in) or an
  access token (Git over HTTPS, and any script using the token). There is
  no SSH transport.

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
  repository's secrets** (`--secrets`), and **record a deployment when it
  ships to a target** (`--ship`).

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

A step's container has full outbound network access, the same as any
other container on the host's Docker network: nothing in Gitman isolates
a pipeline's network access per ref or per repository. This is what lets
a step push an image to a registry, deploy over SSH, or upload a build
artifact — see [Pipelines](../../README.md#pipelines) for examples — and
it is also why `--docker` and, to a lesser extent, unrestricted outbound
access are only for refs whose pushers are trusted.

## Secrets

See [Secrets](../ci/secrets.md) — encrypted with `GITMAN_SECRET_KEY`,
gated per-ref by `--secrets`, masked in stored run output, and never
placed on a command line.

## Cross-site request forgery

Every state-changing request (anything but GET/HEAD/OPTIONS) is checked
against where it came from: the browser's `Sec-Fetch-Site` header, or
failing that its `Origin` header, must say the request is same-origin. A
cross-site request — from another site's form or script — is refused
before it reaches a handler. A request carrying neither header does not
come from a browser, so there is no forgery to prevent; this, together
with the session cookie's `SameSite=Lax`, is the whole defense — there
are no separate per-form CSRF tokens to keep in sync.

## Cookies and proxies

The session cookie's `Secure` attribute follows `GITMAN_PUBLIC_URL`'s
scheme automatically. If Gitman is behind a reverse proxy, set
`GITMAN_TRUSTED_PROXIES` to the proxy's actual reachable address —
otherwise the sign-in rate limiter (and anything else keyed on client IP)
treats every proxied client as the same one.
