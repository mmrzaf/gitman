# Roadmap

Gitman is a small, self-hosted Git server and Docker CI/CD system. This file
tracks outstanding work, not the history of implementation phases. Shipped
changes belong in [CHANGELOG.md](CHANGELOG.md).

## Release readiness

- [ ] Build and test the exact release candidate with the supported Go and PostgreSQL versions.
- [ ] Pass Git transport, worker, Docker, browser and backup/restore integration tests.
- [ ] Verify PostgreSQL persistence through container recreation and restore on a separate host.
- [ ] Rehearse failures (worker loss, database interruption, disk exhaustion) on disposable hosts.
- [ ] Run representative pipelines against non-production deployments.
- [ ] Verify published binaries, container images, source archive and release notes before tagging.

## Later

- Evaluate retention of historical pipeline and repository-operation payloads.
- Investigate receive-pack quarantine directories left after abrupt termination.
- Design a clearer protocol outcome for pushes applied successfully but recorded late.
- Consider pagination for repositories with very large numbers of refs.
- Consider lifecycle tracking for child containers started directly through the Docker socket.
- Replace unnecessary time-based waits in tests where deterministic synchronization is possible.

## Out of scope for now

SSH transport and pipeline artifacts are intentionally not part of the current
product. See [Not added, on purpose](README.md#not-added-on-purpose).
