# Roadmap

What's planned for Gitman, and why. Tick an item off when it ships, and
move it into [CHANGELOG.md](CHANGELOG.md) with its release.

## Next: beta 22

Refs and history: managing branches, tags and revisions from the web.

- [ ] **Delete a branch or tag from the web.** A Delete button on each
  row of Overview's Branches and Tags, shown only when a push deleting
  it would be accepted: never the default branch, and only where the
  ref's rule allows deleting. It's recorded like a push, so it shows in
  History's Activity. The confirm for a branch says whether it's merged
  into the default branch.
- [ ] **Compare any two refs.** Two ref pickers (branch, tag or commit)
  on the compare page, so tag to tag shows what a release added. Today
  it's reachable only from a branch row, against the default branch.
  - [ ] Ahead and behind the default branch on each branch row.
- [x] **History as its own section.** Overview · Files · History · Runs ·
  Settings. Files loses its Code and History tabs; a file's history is a
  link into History, filtered to that path.
  - [x] Commits: a ref's log, with each commit's run status, the
    branches and tags at it, and a merge marker.
  - [x] Activity: every ref change from `push_updates` (created, pushed,
    force-pushed, deleted, tag moved), with who, old → new, and the run
    it started.
- [ ] **Record refused pushes.** A push Gitman refuses shows in Activity
  with its reasons. Git reports only "pre-receive hook declined" per
  ref, and the `remote:` lines saying why are easy to lose: an agent
  pushing v0.1.1–v0.1.3 to waiotech reported no reason at all.
- [ ] **More in the Runs table.** The commit, how long the run took, who
  started it, and the target it shipped to.
- [ ] **Download a ref as an archive.** A `.tar.gz` or `.zip` of any
  branch, tag or commit, streamed with `git archive`, following the
  repository's read access, from a Download button next to the ref picker
  on Files. Beta 1–20 had this; the rewrite doesn't yet.
- [ ] **Show a deployment's version only when it isn't the commit.** A
  branch deploy shows the same commit twice on the Overview and Home.

## Later

- [ ] **Limits for pipeline steps.** Memory and CPU limits in `.env`,
  applied to every step container, as beta 20 had.
  - [ ] Decide what they must bound: a step's limits don't reach a
    `docker build` it runs, which is where pipelines like waiotech's do
    their work. Bounding builds needs limits at the Docker level.
- [ ] **Settings people actually miss.** Add to `.env`, or to Gitman's
  own settings, only what a real deployment needed.
  - [ ] Collect the candidates: default run timeout, runs a worker takes
    at once, sign-in length.

## Pipelines on the server

Not Gitman changes, but open work on the projects it runs.

- [ ] waiotech: add `timeout: 60m`, so a first, uncached build can't be
  stopped mid-deploy by the 30-minute default.
- [ ] waiotech: tick **run** on the `develop` rule, so pushes deploy
  staging.
- [ ] Prune old images on the host: every run leaves `<app>:<version>`
  images behind.
- [ ] cerv: tag `cerv:1.0` from a `cerv:1.0.x` build, since waiotech's
  pipeline requires it.

## Postponed

Not now, but not ruled out. The README currently lists both as not added.

- [ ] **Pipeline artifacts.**
- [ ] **SSH.**

## Decided against

Things Gitman leaves out on purpose are listed in the README's [Not
added, on purpose](README.md#not-added-on-purpose); an idea there needs a
new reason before it comes back here.
