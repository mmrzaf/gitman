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

Add RSA, ECDSA, Ed25519, or OpenSSH security keys from **SSH Keys** after the operator enables SSH transport. Gitman validates and canonicalizes each key, displays its SHA-256 fingerprint, rejects insecure DSA and certificate records, and atomically regenerates the managed `authorized_keys` file. Shell access is not provided; keys are restricted to Git forced commands.

## Browser source features

Repository pages provide:

- Branch, tag, and immutable commit browsing with source context preserved between pages.
- File trees, breadcrumbs, syntax-highlighted blobs, line/range permalinks, Raw/Download/Copy actions, and keyboard-driven Go to File.
- Commit history, commit detail pages, unified diffs, rename/delete/binary handling, and CI status linked to the exact commit.
- Safe rendered root READMEs with repository-relative links.
- ZIP and TAR.GZ source downloads.
- Clone commands for HTTP and SSH.
- A small repository shortcut set: `t`, `g f`, `g c`, `g i`, and `?`.

## CI UI

Members can view exact run status, queue and execution timing, structured step logs with incremental live updates, raw-log search/follow/wrap controls, exact pipeline configuration, outcome reasons, retry history, and nested artifacts. Owners and write collaborators can run CI manually, cancel queued or running jobs, and retry completed jobs against the same commit. Gitman manages the durable post-receive trigger automatically; owners manage repository secrets and trusted-ref policy.

Repository owners also have a dedicated **Settings** page for description, visibility, and safe deletion. Repositories with queued or running CI jobs—or a cancelled job whose worker is still stopping—must be settled before deletion.
