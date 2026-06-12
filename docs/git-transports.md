# Git transports

## Git over HTTP

Git smart HTTP is available from the web process:

```text
<public-url>/<owner>/<repository>.git
```

Example:

```bash
git clone https://git.example.com/alice/project.git
```

Authentication rules:

| Operation | Public repository | Private repository |
| --- | --- | --- |
| Clone and fetch | Anonymous allowed | Owner or `read`/`write` collaborator |
| Push | Owner or `write` collaborator | Owner or `write` collaborator |

For authenticated operations, Git uses HTTP Basic authentication. Use your Gitman username and a personal access token as the password. Account passwords are not accepted for Git Smart HTTP.

## Git over SSH

SSH transport is optional and depends on host OpenSSH configuration. The clone form is:

```bash
git clone git@git.example.com:alice/project.git
```

The SSH username defaults to `git` and is controlled by `GITMAN_SSH_USER`. The hostname displayed in the UI comes from `GITMAN_SERVER_HOST`.

SSH keys authenticate Gitman users through generated OpenSSH forced commands. There is no shell access. The allowed commands are:

- `git-upload-pack`
- `git-receive-pack`
- `git-upload-archive`

Unlike public HTTP clone, SSH always uses an authenticated key.

## Source archives

The browser UI exposes source archives for the selected ref:

```text
/<owner>/<repository>/archive/zip?ref=<branch-or-tag>
/<owner>/<repository>/archive/tar.gz?ref=<branch-or-tag>
```

Archives follow repository visibility: public source archives are public; private source archives require repository access.
