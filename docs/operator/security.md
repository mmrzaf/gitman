# Security model and deployment hardening

## CI worker privilege boundary

The CI worker can mount `/var/run/docker.sock`. Access to that socket is effectively control of the Docker host. Ordinary job containers do not receive the socket. Pipelines with `docker: true` receive it only when the operator enables `GITMAN_CI_ALLOW_DOCKER_SOCKET=true` and the repository owner enables Docker socket trust for the matching branch, tag, or ref pattern. Those jobs effectively control the Docker host. Docker isolation is not a complete hostile multi-tenant boundary.

Before allowing untrusted repository writers:

- Run the worker on a dedicated machine or VM.
- Put Docker storage and Gitman data on filesystems with kernel-enforced quotas.
- Keep `GITMAN_CI_NETWORK=none` unless outbound access is explicitly required.
- Keep worker concurrency low.
- Pre-pull only approved images.
- Run jobs as a numeric non-root UID:GID.
- Leave `GITMAN_CI_ALLOW_DOCKER_SOCKET=false` unless Docker builds are restricted to trusted repositories on a dedicated runner.
- Monitor Docker storage, Gitman data usage, free filesystem inodes, and worker logs.
- Keep the worker storage reserve (`GITMAN_CI_STORAGE_MIN_FREE_BYTES` / `GITMAN_CI_STORAGE_MIN_FREE_INODES`) enabled. Gitman pauses new claims below the reserve, but kernel-enforced quotas remain the real containment boundary.

## Repository-writer trust

A write collaborator can push a changed `.gitman-ci.yml` and manually trigger CI. Gitman always reads `.gitman-ci.yml` from the exact commit being run. Non-default branches and tags do not auto-run by default and do not receive secrets unless the owner adds a matching trust rule. Assume repository writers can read every secret exposed to jobs for that repository. Secret masking in logs is defense in depth only.

## Git HTTP authentication

Git Smart HTTP uses HTTP Basic for client compatibility, but the password field must be a personal access token. Account passwords are not accepted for Git clone, fetch, or push. Public read-only HTTP clones remain public for public repositories. New personal access tokens have a finite lifetime (30, 90, 180, or 365 days), an explicit repository scope, and an approximate last-use timestamp. `repo:read` tokens can clone/fetch and use read-only repository APIs; `repo:write` tokens can additionally push. New tokens default to read-only. Tokens that existed before beta 18 receive a 90-day rotation deadline and retain `repo:write` compatibility during migration. PATs are not accepted as browser-session credentials, preventing a read-only token from reaching HTML mutation flows through CSRF forms.

Admin password resets revoke all existing browser sessions and personal access tokens for the user. Successful and failed login checks, self-registration, admin user/password operations, token and SSH-key changes, repository lifecycle/settings changes, collaborator changes, and CI secret/trusted-ref changes are written to Gitman's database audit trail. Audit metadata never includes token plaintext, passwords, SSH private material, or CI secret values. Requests rejected by the login rate limiter are not appended repeatedly after the source is already blocked.

## Public repositories

Public source is browseable anonymously and cloneable anonymously over HTTP. CI logs and artifacts remain limited to owners and explicit collaborators because they can contain sensitive build output.

## HTTPS and cookies

For any non-local deployment:

```bash
export GITMAN_PUBLIC_URL=https://git.example.com
export GITMAN_FORCE_SECURE_COOKIES=true
export GITMAN_TRUST_PROXY_HEADERS=true
```

Enable proxy-header trust only when requests cannot bypass the trusted reverse proxy. Gitman sets strict SameSite cookies and sends security headers, including HSTS when HTTPS is detected or secure cookies are forced.

Browser login attempts are throttled in memory per normalized username/client-IP pair and per client IP. Proxy client IP headers are ignored unless `GITMAN_TRUST_PROXY_HEADERS=true`.

## Registration

Public account registration is off by default. Keep `GITMAN_ALLOW_REGISTER=false` unless open registration is intentional.

## Secrets key management

Keep `GITMAN_SECRET_KEY` outside the data directory in a secret manager. Back it up separately. Rotation requires an application-level re-encryption plan; changing the value directly makes stored secrets unreadable.

## Backups are sensitive

Full backups contain the SQLite database, repositories, and artifacts. Protect backups as production data. Generated `authorized_keys`, CI caches, and temporary workspaces are intentionally excluded; `authorized_keys` is rebuilt from the database on web startup.

## Release hygiene

Never distribute `.data/`, `data/`, SQLite files, repositories, artifacts, CI logs, generated `authorized_keys`, `.git/`, or local credentials in release archives. `.gitignore` is not a packaging control.

## Security activity and audit review

Authenticated users can open **Security** in the main navigation to review the most recent account-relevant events. The page includes actions performed by the account plus events that target it, such as failed sign-ins and administrator password resets. It deliberately renders a small allow-listed summary of audit metadata rather than dumping raw metadata into the browser.

Operators can inspect the full bounded audit stream with:

```bash
gitman admin audit --limit 100
```

For log ingestion, use newline-delimited JSON:

```bash
gitman admin audit --limit 500 --json
```

Audit output is sensitive because it can contain usernames, source IPs, repository identifiers, token/key names, and CI secret **names**. It does not contain PAT plaintext, account passwords, SSH private material, or CI secret values.
