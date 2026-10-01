# CLI reference

The `gitman` binary picks its role from its first argument.

## Top-level commands

| Command | Purpose |
|---|---|
| `gitman web` | Run the web process (Git over HTTP and the web interface). |
| `gitman worker` | Run the worker process (claims and runs pipelines). |
| `gitman admin ...` | Manage people, tokens, repositories and rules. |
| `gitman hook ...` | Run a Git hook. Started by Git itself, not by hand. |
| `gitman check <file>` | Validate a `.gitman.yml` pipeline file. |
| `gitman restore <backup-directory>` | Verify and restore a maintenance snapshot into empty destinations. |
| `gitman version` | Print the Gitman version (`gitman <version>`). |

## `gitman admin`

Run inside the `web` container in the supported Compose setup:
`docker compose exec web gitman admin ...`.

```
gitman admin person add [--admin] <username>
gitman admin person list | disable | enable | reset-password <username>
gitman admin person revoke-all <username>
gitman admin person role <username> admin|member
gitman admin token create [--write] [--days N] [--repos name[,name]] <username> <name>
gitman admin repo create [--description TEXT] [--default-branch NAME] <name>
gitman admin repo list | delete <name> | sync <name>
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
gitman admin worker list
gitman admin worker cleanup
gitman admin operation list | recover
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
- `admin worker cleanup` reclaims step containers and workspaces left
  behind by a worker that was killed outright (SIGKILL, OOM) rather than
  shut down normally. See [Troubleshooting](../troubleshooting.md).
- `admin migrate` runs pending database migrations by hand; normally
  unnecessary, since `web` and `worker` both run migrations automatically
  on start.
