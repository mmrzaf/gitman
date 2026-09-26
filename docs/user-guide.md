# User guide

## Signing in

Sign in with the username and password an admin created for you (or that
you reset). There is no self-registration — every account is created by
an admin with `gitman admin person add`.

## Home

Lists every repository you can read. Anyone signed in can create a new
one from here; there are no organizations or namespaces, just one flat
list of what's readable to you.

## A repository

Its nav has three items:

- **Overview** — its branches and tags, what's currently deployed to
  each target (if the pipeline defines any), and a timeline of recent
  activity.
- **Files** — browse the tree at any branch, tag or commit; jump to a
  file by path with the go-to-file finder. A commit's own page (its diff
  and metadata) and a comparison between two refs are reached from links
  here, not from the nav.
- **Runs** — every run of this repository's pipeline, newest first,
  paged.
- **Settings** — admins only: description, ref rules, secrets, who may
  read the repository and push to a ref no rule matches, and deletion.

### Read access

Each repository is either readable by **everyone** signed in, or
**restricted** to its explicit readers and admins — set on its Settings
page's **Access** tab. To anyone who cannot read a restricted repository,
it does not exist: it is left out of Home, search, activity, and
anywhere else a list of repositories or their runs appears, and Git
itself refuses to clone, fetch or push to it.

## Runs

Each push that a ref rule allows to run the pipeline starts a run. A
run's page shows each step, its output, and whether it passed. From here
you can:

- **Run again** — re-run the same commit.
- **Cancel** — stop a run in progress.

Anyone allowed to push to the ref can start or cancel a run; a run of a
bare commit with no ref (nothing matched) can be started or cancelled by
any member who can read the repository.

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
