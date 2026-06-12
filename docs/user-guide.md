# Repository user guide

## Accounts

Operators normally create accounts with the admin CLI. Self-registration appears only when the operator sets `GITMAN_ALLOW_REGISTER=true`.

## Repositories

Authenticated users can create and delete repositories from **Repositories**. Each repository has:

- A name containing letters, numbers, dashes, or underscores.
- An optional description, up to 500 characters.
- A public or private visibility setting chosen at creation time.

Deleting a repository removes its active bare repository, CI logs, CI artifacts, and CI cache after the database record is deleted. Treat deletion as destructive.

## Public and private source

Public repository source can be browsed anonymously in the web UI and cloned anonymously over HTTP. Private repository source is limited to the owner and explicit collaborators.

CI logs and artifacts are member-only even when the source repository is public.

## Collaborators

Only the owner manages collaborators. Access levels are:

| Access | Pull and browse private source | Push | View CI logs and artifacts | Run CI manually |
| --- | --- | --- | --- | --- |
| `read` | Yes | No | Yes | No |
| `write` | Yes | Yes | Yes | Yes |

A write collaborator can modify `.gitman-ci.yml`. Do not inject secrets into a repository unless every write collaborator is trusted with those values.

## Personal access tokens

Create tokens from **Access Tokens**. A token:

- Starts with `gm_`.
- Is displayed only once.
- Is required for authenticated Git over HTTP.
- Can authenticate artifact API downloads using `Authorization: Bearer <token>`.
- Can be revoked from the UI.

Store tokens in a credential manager. Do not commit them.

## SSH keys

Create SSH keys from **SSH Keys** after the operator enables SSH transport. Gitman regenerates a managed `authorized_keys` file when keys are added or deleted. Shell access is not provided; keys are restricted to Git forced commands.

## Browser source features

Repository pages provide:

- Branch and tag selection.
- File tree browsing.
- Blob viewing.
- Commit history.
- ZIP and TAR.GZ source downloads.
- Clone commands for HTTP and SSH.

## CI UI

Members can view run status, logs, and artifacts. Owners and write collaborators can run CI manually. Owners can install or remove the post-receive auto-trigger hook and manage repository secrets.
