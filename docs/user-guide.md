# User guide

## Signing in

Sign in with the username and password an admin created for you (or that
you reset). There is no self-registration — every account is created by
an admin with `gitman admin person add`.

## Home

Lists every repository. Anyone signed in can create a new one from here;
there are no organizations or namespaces, just one flat list.

## A repository

- **Files** — browse the tree at any branch, tag or commit; jump to a
  file by path with the go-to-file finder.
- **Commit** — a single commit's diff and metadata.
- **Compare** — the diff between two refs.
- **Runs** — the pipeline runs triggered by pushes (or started by hand)
  for this repository, and what's currently deployed to each target, if
  the pipeline defines any.
- **Settings** — for people allowed to manage this repository: ref rules,
  secrets, and deletion.

## Runs

Each push that a ref rule allows to run the pipeline starts a run. A
run's page shows each step, its output, and whether it passed. From here
you can:

- **Run again** — re-run the same commit.
- **Cancel** — stop a run in progress.

Anyone allowed to push to the ref can start or cancel a run; a run of a
bare commit with no ref (nothing matched) can be started or cancelled by
any member.

## Account

Under your username, top-right of every page:

- **Access tokens** — create and revoke the tokens Git uses in place of a
  password. A read token can clone and fetch; a write token can also
  push.
- Change your password.

## Admins

Admins additionally see **People**, to add, disable, enable, and change
the role of accounts. People are disabled, never deleted, so their name
stays attached to what they did.

See [Security model](operator/security.md) for how ref rules and roles
fit together, and the [CLI reference](reference/cli.md) for doing any of
this from the command line instead.
