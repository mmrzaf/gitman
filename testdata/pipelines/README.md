# Pipeline fixtures

These are real pipelines, copied from the projects that run them, used as
golden fixtures for the pipeline parser and the worker's step execution.

- **sms-gateway.gitman.yml** — one target (`production`, matched by `v*`
  tags). Build and check steps run on every push; the deploy step, gated
  by `when: production`, runs only on a release tag.
- **cerv.gitman.yml** — no targets. Every step is gated by `when: tag`, so
  a branch push runs nothing and a tag push runs all three steps.
- **waiotech.gitman.yml** — two targets: `staging` (branch `develop`) and
  `production` (`v*` tags), each with its own environment overrides. Every
  step is gated by `when: target`, so a push that matches neither, such as
  a backup tag, runs nothing.
