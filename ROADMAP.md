# Roadmap

What's planned for Gitman, and why. Tick an item off when it ships, and
move it into [CHANGELOG.md](CHANGELOG.md) with its release.

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
