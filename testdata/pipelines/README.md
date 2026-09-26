# Pipeline fixtures

These are real pipelines, used as golden fixtures for the pipeline parser
and the worker's step execution.

- **sms-gateway.gitman.yml** — one target (`production`, matched by any
  tag). Build and verify steps run on every push; the deploy step, gated by
  `when: production`, runs only on a tag push and writes a summary.
- **cerv.gitman.yml** — no targets. Every step is gated by `when: tag`, so
  a branch push runs nothing and a tag push runs all three steps.
- **waiotech.gitman.yml** — two targets: `staging` (branch `develop`) and
  `production` (any tag), each with its own environment overrides. The
  build steps are gated by `when: target`, so they run for either target
  and are skipped for a push that matches neither. The deploy step reads
  `$GITMAN_TARGET` to report which target it shipped to.
