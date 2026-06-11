# Security model and deployment hardening

## CI worker privilege boundary

The CI worker mounts `/var/run/docker.sock`. Access to that socket is effectively control of the Docker host. Job containers do not receive the socket, but the worker remains privileged infrastructure and Docker isolation is not a complete hostile multi-tenant boundary.

Before allowing untrusted repository writers:

- Run the worker on a dedicated machine or VM.
- Put Docker storage and Gitman data on filesystems with kernel-enforced quotas.
- Keep `GITMAN_CI_NETWORK=none` unless outbound access is explicitly required.
- Keep worker concurrency low.
- Pre-pull only approved images.
- Run jobs as a numeric non-root UID:GID.
- Monitor Docker storage, Gitman data usage, and worker logs.

## Repository-writer trust

A write collaborator can push a changed `.gitman-ci.yml` and manually trigger CI. Assume repository writers can read every secret exposed to jobs for that repository. Secret masking in logs is defense in depth only.

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

## Registration

Public account registration is off by default. Keep `GITMAN_ALLOW_REGISTER=false` unless open registration is intentional.

## Secrets key management

Keep `GITMAN_SECRET_KEY` outside the data directory in a secret manager. Back it up separately. Rotation requires an application-level re-encryption plan; changing the value directly makes stored secrets unreadable.

## Backups are sensitive

Full backups contain the SQLite database, repositories, artifacts, and generated `authorized_keys` when present. Protect backups as production data. CI caches and temporary workspaces are intentionally excluded.

## Release hygiene

Never distribute `.data/`, `data/`, SQLite files, repositories, artifacts, CI logs, generated `authorized_keys`, `.git/`, or local credentials in release archives. `.gitignore` is not a packaging control.
