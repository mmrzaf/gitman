# CLI reference

## General help

```bash
gitman
gitman version
gitman --version
```

## Start web

```bash
gitman web
gitman web --port 8081
```

## Start CI worker

```bash
gitman worker
```

## SSH forced-command handler

```bash
gitman serve <keyID>
```

This command is invoked by generated OpenSSH forced commands. Users should not invoke it directly.

## User administration

```bash
read -rsp 'Password: ' USER_PASSWORD; printf '\n'
printf '%s\n' "$USER_PASSWORD" | gitman admin users create alice
unset USER_PASSWORD

read -rsp 'New password: ' USER_PASSWORD; printf '\n'
printf '%s\n' "$USER_PASSWORD" | gitman admin users reset-password alice
unset USER_PASSWORD

gitman admin users delete alice
```

Deleting a user removes their database record, repositories, artifacts, caches, and SSH-key entries after moving active repository files out of the live namespace.

## Backups

```bash
gitman admin repos backup <destination>
gitman admin repos backup-all <destination>
gitman admin repos configure-all
```

See [backups and upgrades](../operator/backups-and-upgrades.md).

`configure-all` verifies managed repository storage, applies the configured Git receive-pack input ceiling, and reconciles Gitman's managed CI post-receive hook. It refuses to overwrite an operator-owned hook.

## Operational status

```bash
gitman admin status
```

Reports the Gitman version, current database schema, repository/artifact storage readiness, recent CI worker counts, active jobs, pending queue depth/oldest queued time, and production-configuration warnings. The command exits non-zero when core readiness checks fail, CI status cannot be queried, or pending CI work has no healthy worker.

## Audit trail

```bash
gitman admin audit
gitman admin audit --limit 250
gitman admin audit --json
```

The audit command prints newest-first security events. `--json` emits newline-delimited JSON for ingestion into log tooling. Output can include usernames, source IPs, repository/token/key identifiers, and non-secret event metadata, so treat it as security-sensitive operational data.
