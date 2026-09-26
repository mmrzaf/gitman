# Gitman documentation

- [Overview](overview.md) — what Gitman is and isn't.
- [Architecture](architecture.md) — web, worker and PostgreSQL.
- [Getting started](getting-started.md) — the quickest supported install path.
- [User guide](user-guide.md) — accounts, repositories, and following runs.
- [Git transports](git-transports.md) — cloning and pushing over HTTPS.
- [Development](development.md) — building and testing from source.

### CI/CD

- [Pipelines overview](ci/README.md)
- [Pipeline configuration reference](ci/configuration.md) — the `.gitman.yml` schema.
- [Secrets](ci/secrets.md)

### Reference

- [CLI reference](reference/cli.md)

### Operating Gitman

- [Configuration reference](operator/configuration.md) — every setting, Compose and standalone.
- [Docker deployment](operator/docker.md) — the supported Traefik-fronted Compose setup.
- [Building from source](operator/source-install.md)
- [Backups and upgrades](operator/backups-and-upgrades.md)
- [Security model](operator/security.md)

### Other

- [Troubleshooting](troubleshooting.md)
- [Release checklist](maintainers/release-checklist.md) — for maintainers cutting a release.
