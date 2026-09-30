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

Its nav has five items:

- **Overview** — its branches and tags, what's currently deployed to
  each target (if the pipeline defines any), and a timeline of recent
  activity.
- **Files** — browse the tree at any branch, tag or commit; jump to a
  file by path with the go-to-file finder. A commit's own page (its diff
  and metadata) and a comparison between two refs are reached from links
  here, not from the nav. **History** on a file or directory opens the
  History section, filtered to that path.
- **History** — two tabs:
  - **Commits** is a branch's, tag's or commit's log, newest first, with
    each commit's latest run, the branches and tags now at it, and a
    marker on merges. Pick the ref at the top; a path filter, set from
    Files, shows only the commits that touched that path.
  - **Activity** is every change to the repository's refs (created,
    pushed, force-pushed, deleted, or a tag moved) with who made it, the
    commits it moved from and to, and the run it started, together with
    changes to its settings. A push Gitman refused shows too, with who
    pushed and why each ref was refused: Git tells the pusher only that
    the hook declined it, and the reasons are easy to lose among its
    output.
- **Runs** — every run of this repository's pipeline, newest first,
  paged.
- **Settings** — admins only: description, default branch, ref rules,
  secrets, who may read the repository and push to a ref no rule matches,
  and deletion.

### Deleting a branch or tag

**Delete** on a row of the Overview's Branches and Tags removes that ref.
It is there only where a push deleting it would be accepted: never on the
default branch, and only where the ref's rule lets you push to it and
allows deleting. Gitman checks again when you confirm, by the same rules a
push is held to. A branch's confirmation says whether it is merged into the
default branch.

A delete from the web is recorded as a push is: it shows in History's
Activity under your name, brings the branch and tag lists up to date, and
cancels the ref's queued runs. Gitman has no undo for it; a tag can be
pushed again, and a branch's commits stay reachable only from what else
points at them.

### Default branch

Every repository has one default branch, named when it is created
(`main` if left blank). It is what `git clone` checks out, what **Files**
opens, and what each branch is compared with on the Overview. A push may
not delete it.

Gitman never changes it by itself. An admin can move it to any branch the
repository has, on the Settings page's **General** tab or with
`gitman admin repo default-branch <repo> <branch>`.

Since it cannot be deleted, the default branch is missing only until it
is first pushed. Until then, the Overview and Files say so and link to
the branches that do exist, instead of links that lead nowhere.

### Read access

Each repository is either readable by **everyone** signed in, or
**restricted** to its explicit readers and admins — set on its Settings
page's **Access** tab. To anyone who cannot read a restricted repository,
it does not exist: it is left out of Home, search, activity, and
anywhere else a list of repositories or their runs appears, and Git
itself refuses to clone, fetch or push to it.

## Runs

A run runs the pipeline in `.gitman.yml` at the root of a branch or
tag's commit, on a worker. It starts in one of three ways:

- **A push** to a branch or tag whose ref rule has **run** on. A push
  that starts no run says why, in the output of `git push`.
- **By hand, from a branch or tag** — the **Run** button on the Runs
  page (pick the branch or tag; the default branch comes first), or the
  run button on its row of the Overview. It runs the ref's latest commit
  exactly as a push to it would, with its rule's target, secrets and
  Docker.
- **Again** — **Run again** on a run's page re-runs its commit for the
  same branch or tag, under that ref's rule. Run again on an older run
  is how you roll back.

A run waits in the queue until a worker claims it. When no worker is
online, a queued run's page and Home say so.

A run's page shows each step, its output, and whether it passed. From
there you can:

- **Run again** — re-run the same commit, for the same ref.
- **Cancel** — stop a run in progress.

Anyone allowed to push to the ref can start or cancel its runs.

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
