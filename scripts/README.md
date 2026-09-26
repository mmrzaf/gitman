# Scripts

Two scripts exercise a built Gitman against real dependencies — nothing
here is mocked. Both are safe to run repeatedly; each drops and recreates
its own database and working directory first.

## `e2e.sh`

Builds the binary, runs web and a worker against real PostgreSQL and
real Docker, and pushes real Git commits over HTTP to drive real
pipeline runs: a normal pipeline that passes and ships, `?raw` file
serving, a run cancelled mid-step, three deliberately malicious
pipelines (a symlinked summary file, NUL bytes in step output, an
image name shaped like a Docker flag), and a worker killed mid-step
followed by `gitman admin worker cleanup`.

Requires Go, git, curl, psql, and a Docker daemon with `alpine:3.20`
already pulled — Gitman never pulls images itself, and neither does
this script. PostgreSQL is expected at `127.0.0.1:5432` as `postgres`;
override with `PGHOST`, `PGPORT` and `PGUSER`. Creates and drops a
database named `gitman_e2e`, and listens on `GITMAN_E2E_PORT` (18080 by
default). It checks only the step containers of the instance it starts,
so it can run on a Docker host shared with another Gitman.

```
scripts/e2e.sh
```

## `browser.sh` + `browser.mjs`

Starts the same kind of server, seeds it with what a small team's
instance holds — people, four repositories, rules, secrets, tokens, and
runs that passed, failed and shipped to two targets — and drives it with
a real, headless Chromium:

- live pages: Home and a repository updating by themselves, a Run page
  that streams output, follows the run from step to step and follows new
  output, keyboard focus kept across a live update, and cancelling
  through the confirmation dialog;
- every interactive component, by keyboard: tabs and the address they
  keep (Back included), menus, dialogs, confirmations, toasts, the
  command palette, the file finder, copy buttons, inline editing, and
  the log view;
- an accessibility scan with axe-core (WCAG 2.1 A and AA) of every screen,
  and of its open dialogs and menus, in the light and the dark theme;
- no screen scrolling sideways at phone width;
- every page with JavaScript disabled, and the plain-HTML fallbacks: ref
  switching, tab and dialog addresses, and saving a form.

It then takes a screenshot of every screen in both themes, and of the
busiest ones at phone width, into `.data/browser/screenshots`.

Requires everything `e2e.sh` does, plus Node with Playwright, axe-core
and a Chromium:

```
cd scripts && npm install && npx playwright install chromium
scripts/browser.sh
```

To use a Chromium already on the machine instead, set
`PLAYWRIGHT_EXECUTABLE_PATH` to it. The server listens on
`GITMAN_BROWSER_PORT` (18081 by default), with its own database,
`gitman_browser`, so the two scripts can run one after another without
clearing state by hand.
