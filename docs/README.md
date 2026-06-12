# Gitman documentation

Gitman is a lightweight self-hosted Git forge for small teams and private infrastructure. It provides repository hosting, a browser UI, Git over HTTP, optional Git over SSH, collaborators, personal access tokens, and an optional Docker-backed CI worker.

Gitman is intentionally not a multi-tenant SaaS platform and not a general-purpose CI runner fleet. The CI worker executes repository-controlled code and must be operated as privileged infrastructure.

## Choose your path

| You are... | Start here | Then read |
| --- | --- | --- |
| A repository user | [Getting started](getting-started.md) | [User guide](user-guide.md), [Git transports](git-transports.md) |
| Writing `.gitman-ci.yml` | [CI overview](ci/README.md) | [CI configuration](ci/configuration.md), [secrets](ci/secrets.md), [artifacts](ci/artifacts.md) |
| Installing or operating Gitman | [Docker deployment](operator/docker.md) or [source install](operator/source-install.md) | [configuration](operator/configuration.md), [security](operator/security.md), [backups and upgrades](operator/backups-and-upgrades.md) |
| Enabling SSH | [SSH transport setup](operator/ssh.md) | [Git transports](git-transports.md) |
| Integrating build outputs | [Artifact HTTP API](reference/artifact-http-api.md) | [CI artifacts](ci/artifacts.md) |
| Contributing to Gitman | [Development guide](development.md) | [Architecture](architecture.md), [release checklist](maintainers/release-checklist.md) |
| Diagnosing a failure | [Troubleshooting](troubleshooting.md) | Relevant operator or CI page |

## Core safety rules

1. Expose the web service through HTTPS before using it beyond a trusted local network.
2. Treat the CI worker as privileged host infrastructure because it controls the Docker daemon through `/var/run/docker.sock`.
3. Run the CI worker on a dedicated runner host or VM before allowing untrusted repository writers.
4. Pre-pull approved CI images. Gitman uses `docker run --pull never`.
5. Assume any repository writer can access any CI secret injected into that repository's jobs.
6. Keep `GITMAN_SECRET_KEY` in a deployment secret manager and preserve it across restores. Losing it makes stored CI secrets unreadable.
7. Never publish runtime state such as `.data/`, `data/`, SQLite files, repositories, artifacts, or `.git/` in source-release archives.

## Scope summary

Gitman supports:

- Browser login and optional self-registration.
- Public and private repositories.
- Repository owners plus `read` and `write` collaborators.
- Git smart HTTP using personal access tokens.
- Optional host OpenSSH integration using generated forced commands.
- Branches, tags, commit lists, file browsing, source archives, CI logs, and nested artifacts.
- Root-level `.gitman-ci.yml` pipelines executed by a separate worker.
- Repository backups and full backups from the admin CLI.

Gitman does not currently provide:

- A hosted SaaS control plane.
- A distributed runner scheduler.
- Kernel-enforced storage quotas.
- A built-in restore command.
- A stable general-purpose REST API for repository administration.
