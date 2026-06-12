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

`configure-all` applies the configured Git receive-pack input ceiling to existing managed repositories.
