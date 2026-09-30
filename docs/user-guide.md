# User guide

## Signing in

Sign in with the username and password an admin created for you (or that
you reset). There is no self-registration — every account is created by
an admin with `gitman admin person add`.

## Home

Two lists on the left: every repository you can read, with its description,
the latest commit on its default branch and how that ran (or the run in
progress); and **Deployed**, one row for every target something is live on,
with how many commits the default branch has gained since. On the right, a
feed of what has been happening, under **Needs attention** when something
does. Anyone signed in can create a new repository from here; there are no
organizations or namespaces, just one flat list of what's readable to you.

**Needs attention** appears only when something does:

- the latest run of a default branch failed;
- runs are queued and no worker is online;
- a target is behind its default branch;
- a push was refused in the last week, with the reason.

## A repository

Its nav has four items:

- **Overview** — what is deployed to each target (if the pipeline
  defines any) and how many commits each lacks, its branches and tags, the
  clone address, the latest runs of the default branch, and the
  repository's activity. Each branch says how
  far it is ahead of and behind the default branch. A deployment shows its
  version only when that says more than its commit: a tag's name does, and
  a branch's version is its commit, so it shows the commit once. The newest
  tags are listed, with a link to all of them.
- **Commits** — a branch's, tag's or commit's commits, newest first, each
  with its latest run, the branches and tags now at it, and a marker on
  merges. To see what differs between two refs, choose the second in
  **Compared with**: see [Comparing refs](#comparing-refs). Choosing a
  ref changes the list in place, and Back still works.

- **Runs** — every run of this repository's pipeline, newest first,
  paged: its branch or tag and commit, who started it, how long it took,
  and the target it shipped to.
- **Settings** — admins only: description, default branch, ref rules,
  secrets, who may read the repository and push to a ref no rule matches,
  and deletion.

### Browsing files

The files of a branch, tag or commit are a step down from where you are
looking at changes, not a section of their own: a branch or tag's name on the
Overview opens its files, **Browse files** is on Commits, and a commit's
page has one too. Jump to a file by path with the
go-to-file finder, and **Commits** on a file or directory lists the commits
that changed it. **Download**, next to the ref picker, gives the whole tree
as a `.tar.gz`.

### Downloading an archive

An archive of any branch, tag or commit is at
`/<repo>/archive/<ref>.tar.gz` or `.zip`; **Download** next to the ref
picker on Files is the `.tar.gz`, and the `.zip` is at its address. You
must be signed in, as for any page: access tokens are for Git.

The file is named `<repo>-<ref>`, with any `/` in the ref written as `-`,
and everything in it is under a folder of that name. A commit is named by
its first seven characters. Gitman streams the archive from Git as it is
made, so a large repository needs no memory for it. Anyone who can read
the repository can download it, and to anyone else it does not exist.
Downloads share the limit on how many clones, fetches, pushes and downloads
run at once; past it Gitman answers "too busy" and a retry succeeds.

### Comparing refs

On **Commits**, choose what to look at, then what to compare it with.
Each is a branch, a tag or a commit: type part of a name to search, or
type a commit's hash. The page then shows, top to bottom:

- how far the first is ahead of and behind the second;
- two tabs: **Commits**, the commits the first has that the second lacks,
  and **Changes**, one list of the files they change. Each file opens to
  its diff; **Expand all** and **Collapse all** open and close every one.

So two tags show what a release added, the default branch against a
branch shows what merging it would bring, and a branch against what is
live on a target shows what has not shipped. A branch's compare button on
the Overview, and the **N commits not shipped** on a deployed target there
or on Home, open it ready-made. **Swap sides** shows the other direction. Leave **Compared
with** empty for the plain list of commits.

### Activity

The Overview lists what has happened to the repository, newest first, and
**All activity** pages through all of it: branches and tags being created,
pushed, force-pushed, moved and deleted, with who did it, where from and to,
and the run it started; runs that finished; what was shipped; and changes to
its settings. A push Gitman refused shows too, with who pushed and why each
ref was refused: Git tells the pusher only that the hook declined it, and
the reasons are easy to lose among its output.

### Deleting a branch or tag

**Delete** on a row of the Overview's Branches and Tags removes that ref.
It is there only where a push deleting it would be accepted: never on the
default branch, and only where the ref's rule lets you push to it and
allows deleting. Gitman checks again when you confirm, by the same rules a
push is held to. A branch's confirmation says whether it is merged into the
default branch.

A delete from the web is recorded as a push is: it shows in Activity
under your name, brings the branch and tag lists up to date, and
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
- **Theme** — follow the system, or always light or dark. It is kept in
  this browser only.

## Admins

Admins additionally see **People**, to add, disable, enable, and change
the role of accounts. People are disabled, never deleted, so their name
stays attached to what they did.

See [Security model](operator/security.md) for how ref rules and roles
fit together, and the [CLI reference](reference/cli.md) for doing any of
this from the command line instead.
