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
| `gitman version` | Print the Gitman version (`gitman <version>`). |

## `gitman admin`

Run inside the `web` container in the supported Compose setup:
`docker compose exec web gitman admin ...`.

```
gitman admin person add [--admin] <username>
gitman admin person list | disable | enable | reset-password <username>
gitman admin person role <username> admin|member
gitman admin token create [--write] [--days N] <username> <name>
gitman admin repo create [--description TEXT] [--default-branch NAME] <name>
gitman admin repo list | delete <name> | sync <name>
gitman admin rule list <repo>
gitman admin rule set [--push everyone|admins|people] [--people a,b] [--force] [--delete]
                      [--run] [--docker] [--secrets] [--ship] <repo> branch|tag <pattern>
gitman admin rule delete <repo> branch|tag <pattern>
gitman admin run cancel <repo> <number>
gitman admin worker cleanup
gitman admin migrate
```

Flags go before the positional arguments.

### Notes

- `admin person add --admin` creates an admin account; the command prints
  the generated password. `disable`/`enable` toggle sign-in without
  deleting the account — people are never deleted.
- `admin token create --write` grants a token push access, not just
  clone/fetch. `--days N` sets an expiry.
- `admin repo sync <name>` rebuilds a repository's ref index from Git —
  needed after restoring `repos/` from a different point in time than the
  database. See [Backups and upgrades](../operator/backups-and-upgrades.md).
- `admin rule set` flags map directly to what a rule grants: `--force`/
  `--delete` (force-push/deletion), `--run` (pushes trigger the
  pipeline), `--docker`/`--secrets`/`--ship` (what a triggered run may
  use). See [Security model](../operator/security.md).
- `admin run cancel` stops a run in progress; the same is available from
  a run's page in the UI to anyone allowed to push to its ref.
- `admin worker cleanup` reclaims step containers and workspaces left
  behind by a worker that was killed outright (SIGKILL, OOM) rather than
  shut down normally. See [Troubleshooting](../troubleshooting.md).
- `admin migrate` runs pending database migrations by hand; normally
  unnecessary, since `web` and `worker` both run migrations automatically
  on start.
