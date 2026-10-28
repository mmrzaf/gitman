# CLI reference

The `gitman` binary picks its role from its first argument.

## Top-level commands

| Command | Purpose |
|---|---|
| `gitman web` | Run the web process (Git over HTTP and the web interface). |
| `gitman worker` | Run the worker process (claims and runs pipelines). |
| `gitman admin ...` | Manage people, tokens, repositories and rules. |
| `gitman hook ...` | Run a Git hook. Started by Git itself, not by hand. |
| `gitman check [file]` | Validate a `.gitman.yml` pipeline file (defaults to `.gitman.yml`). |
| `gitman doctor` | Check config, data, database, Docker and workers without changing anything. |
| `gitman restore <backup-directory>` | Verify and restore a maintenance snapshot into empty destinations. |
| `gitman version` | Print the Gitman version (`gitman <version>`). |

## `gitman admin`

Run database-only admin commands inside the `web` container:
`docker compose exec web gitman admin person ...`.

Commands that read or change the worker's host, such as `admin worker cleanup`,
run in the `worker` container:
`docker compose exec worker gitman admin worker cleanup`.
`admin images ...` reads the database and queues work for a worker, so it can
run in either.

`doctor` needs the Docker socket, so run it in the worker. Maintenance backups
and restores need `pg_dump`/`pg_restore` and a matching native host; run them
there, not in the web image.

```
gitman admin person add [--admin] <username>
gitman admin person list | disable | enable | reset-password <username>
gitman admin person revoke-all <username>
gitman admin person role <username> admin|member
gitman admin token create [--write] [--days N] [--repos name[,name]] <username> <name>
gitman admin token list <username>
gitman admin session list [username]
gitman admin session revoke <id>
gitman admin repo create [--description TEXT] [--default-branch NAME] <name>
gitman admin repo list | delete <name> | sync <name>
gitman admin repo size <name>
gitman admin repo gc <name>
gitman admin repo visibility <name> everyone|restricted
gitman admin repo default-branch <name> <branch>
gitman admin repo default-push [--push everyone|admins|people] [--people a,b] <name>
gitman admin reader add | remove <repo> <username>
gitman admin reader list <repo>
gitman admin rule list <repo>
gitman admin rule set [--push everyone|admins|people] [--people a,b] [--force] [--delete]
                      [--run] [--docker] [--secrets] [--deploy] <repo> branch|tag <pattern>
gitman admin rule delete <repo> branch|tag <pattern>
gitman admin run cancel <repo> <number>
gitman admin worker cleanup
gitman admin worker release --confirm [--run ID] <worker-id>
gitman admin images list [--worker ID] [--class required|built|other] [--json]
gitman admin images missing
gitman admin images prune [--worker ID] [--confirm]
gitman admin images prune --cache [--worker ID] [--confirm] [--keep-storage GiB] [--older-than HOURS]
gitman admin images remove [--confirm] <worker-id> <image>
gitman admin operation list | recover
gitman admin operation abandon --reason TEXT <id>
gitman admin maintenance enable | disable | status
gitman admin maintenance backup <new-directory>
gitman admin migrate
```

Flags go before the positional arguments.

### Notes

- `admin person add --admin` creates an admin account; the command prints
  a temporary password, valid for 24 hours and requiring a change.
  `disable` revokes credentials; `enable` issues a fresh temporary password.
  People are never deleted.
- `admin token create --write` grants a token push access, not just
  clone/fetch. Omit `--repos` for all current and future repositories, or use
  `--repos name[,name]` to limit it to selected repositories. The owner's
  permissions still apply. `--days N` sets an
  expiry between 1 and 365 days; the default is 30. The person must have
  changed their temporary password first.
- `admin repo sync <name>` rebuilds a repository's ref index from Git —
  for an intentional filesystem edit; it is not a substitute for a consistent
  backup. Restore verifies the index without silently rewriting it. See [Backups and upgrades](../operator/backups-and-upgrades.md).
- `admin repo visibility` sets who may read a repository; `restricted`
  limits it to its readers (`admin reader add`/`remove`/`list`) and
  admins. `admin repo default-push` sets who may push to a branch or tag
  no rule matches. See [Repository read
  access](../operator/security.md#repository-read-access).
- `admin repo default-branch` moves a repository's default branch — what
  a clone checks out — to a branch it already has. See [Default
  branch](../user-guide.md#default-branch).
- `admin rule set` flags map directly to what a rule grants: `--force`/
  `--delete` (force-push/deletion), `--run` (pushes trigger the
  pipeline), `--docker`/`--secrets`/`--deploy` (what a triggered run may
  use). See [Security model](../operator/security.md).
- `admin run cancel` stops a run in progress; the same is available from
  a run's page in the UI to anyone allowed to push to its ref.
- `admin token list` shows a person's tokens, their scope, expiry, last
  use and repository selection. The token value itself is only ever shown
  once, by `token create`.
- `admin session list` shows unexpired browser sessions; `revoke` ends one
  by its opaque ID. A person can do the same for their own sessions on the
  account page, and an admin can revoke anyone's there too.
- `admin repo size` walks the bare repository and prints its disk usage;
  `admin repo gc` runs `git gc` under the repository mutation lock and
  records the result in Activity. Pushes to that repository wait while it
  runs. Both are also on the repository's Settings → Maintenance tab.
- `admin worker cleanup` reclaims step containers and workspaces left
  behind by a worker that was killed outright (SIGKILL, OOM) rather than
  shut down normally. See [Troubleshooting](../troubleshooting.md).
- `admin worker release --confirm` is for a worker whose host is gone for
  good. It refuses a worker that is still heartbeating, fails its running
  runs, releases their deployment targets, marks their containers removed
  and stops the worker record. Use it only after confirming the host and
  its containers are gone; `--run ID` limits it to one run.
- `admin images list` shows each Docker host's inventory with its class,
  reason, size, age, in-use/deployed state and whether it is prunable. `--json`
  emits the same structure for scripts; `--worker` selects one worker's host.
- `admin images missing` lists required images no ready worker advertises, with
  the pipelines that require them. **It exits non-zero when anything is
  missing, or when the image facts are incomplete**, so it can guard a
  deployment check. A missing image means a run stays queued; Gitman never
  pulls it.
- `admin images prune` is a dry run by default. `--confirm` queues one durable
  operation for the owning worker and prints its result when the worker finishes
  it (or reports it as pending if no worker claims it in time). It refuses a
  stale inventory. Only built images beyond their keep count are considered;
  required, in-use and deployed images are reported as refused.
- `admin images prune --cache` applies the build-cache policy instead of
  touching images. `--keep-storage` and `--older-than` override the worker's
  policy for this one run; both are between the documented bounds. The action
  never removes images, containers or volumes.
- `admin images remove --confirm <worker-id> <image>` removes one named tag or
  dangling image without force. Required and in-use images are refused even
  with `--confirm`; a deployed image may be removed deliberately. Without
  `--confirm` it only prints what it would do.
- `admin operation recover` retries every pending repository operation.
  `admin operation abandon --reason TEXT <id>` releases a repository whose
  operation recovery has failed repeatedly, but only when the operation
  has a recorded recovery error and no Git receipt proving it already
  changed Git. It leaves Git and database rows untouched for inspection;
  it only clears the mutation block. See
  [Troubleshooting](../troubleshooting.md).
- `admin migrate` runs pending database migrations by hand; normally
  unnecessary, since `web` and `worker` both run migrations automatically
  on start. A binary refuses to start against a schema newer than its own
  migrations.
- `gitman doctor` is read-only and safe beside a live web or worker. It
  exits 0 with only warnings, and 1 if any check fails.
