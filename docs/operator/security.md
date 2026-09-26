# Security model

## People

- Everyone who can sign in can read every repository. There are no
  anonymous users and no per-repository access lists — one flat list of
  repositories, visible to any signed-in person.
- Anyone signed in can create a repository.
- An **admin** additionally manages people (add, disable, enable, change
  role) and every repository's settings: ref rules, secrets and
  deletion. A person is disabled, never deleted, so their name stays on
  what they did.
- Authentication is a username plus either a password (web sign-in) or an
  access token (Git over HTTPS, and any script using the token). There is
  no SSH transport.

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

A ref that no rule matches is unprotected: anyone may push, force-push or
delete it, and pushes to it trigger nothing. A brand new repository is
fully usable immediately — rules only add restriction and grant
pipeline capabilities, they're never required to use a repository at all.

Set rules with `gitman admin rule set` (see the
[CLI reference](../reference/cli.md)) or from a repository's Settings
page.

## Docker access is the sensitive one

Allowing Docker (`--docker`) hands a pipeline the host's Docker socket,
which is effectively root on the worker's host. Grant it only on refs
whose pushers you'd trust with root on that machine — for example `main`
restricted to admins, not every branch. Containers a step starts through
the socket belong to that step; Gitman only stops and removes the step
containers it started itself, never anything else on the host.

The worker process itself always runs as `root` in the supported Compose
setup, specifically so it can reach `/var/run/docker.sock` — this is a
property of how the worker is deployed, not something a ref rule changes.
Step containers run as whatever user their own image specifies; Gitman
does not force a non-root user inside them.

## Secrets

See [Secrets](../ci/secrets.md) — encrypted with `GITMAN_SECRET_KEY`,
gated per-ref by `--secrets`, masked in stored run output, and never
placed on a command line.

## Cookies and proxies

The session cookie's `Secure` attribute follows `GITMAN_PUBLIC_URL`'s
scheme automatically. If Gitman is behind a reverse proxy, set
`GITMAN_TRUSTED_PROXIES` to the proxy's actual reachable address —
otherwise the sign-in rate limiter (and anything else keyed on client IP)
treats every proxied client as the same one.
