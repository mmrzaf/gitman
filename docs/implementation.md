# Clean implementation checklist

Fresh installation; Go/PostgreSQL/SSR; MIT; one trusted worker profile; explicit deploy step; monitored disk limits; refine existing UI. No compatibility adapters.

- [x] 1. Streaming secret redaction
- [x] 2. Live authorization revocation
- [x] 3. Credential races
- [x] 4. Step resource budgets
- [x] 5. Consistent backups/key recovery
- [x] 6. Deployment serialization
- [x] 7. Durable push recording
- [x] 8. Repository operation recovery
- [x] 9. Confirmed Docker cleanup
- [x] 10. Bounded worker recording
- [x] 11. Atomic access settings
- [x] 12. Commit retention refs
- [x] 13. Page execution deadlines
- [x] 14. Bounded SSE
- [x] 15. Corrupt ciphertext handling
- [x] 16. Independent retention budgets
- [x] 17. Bounded log tails
- [x] 18. Bounded Git readers
- [x] 19. Honest comparison states
- [x] 20. Commit-specific CI status
- [x] 21. Worker readiness/image routing
- [x] 22. Streaming raw downloads
- [ ] 23. Real integration/CI coverage
- [x] 24. Scoped/bounded dashboard refresh
- [x] 25. Cursor activity feed
- [x] 26. Permission-aware actions
- [x] 27. Explicit deployments
- [x] 28. Secret environment validation
- [x] 29. Credential lifecycle
- [x] 30. Mobile metadata cards
- [x] 31. Files navigation
- [x] 32. Ordered mobile steps
- [x] 33. Fixed desktop dashboard panels
- [x] 34. Touch/readability scale
- [x] 35. Unified ref/hash validation
- [x] 36. Canonical names/password units
- [x] 37. Root-only URLs
- [x] 38. Remove unused/duplicate CI reads
- [x] 39. Typed partials/service orchestration
- [x] 40. Safe release input
- [x] 41. Isolated test databases
- [x] 42. Authoritative consistent docs
- [x] 43. MIT/portable Compose

## Direction review follow-up

Selected findings 1–11 and 13–15 are implemented. Finding 12, expanding Gitman's
own pipeline, was excluded by the request.

- Retained Docker exit receipts record successful deployments during recovery,
  including after the run was marked failed. Cleanup persists receipts before
  deleting containers. Deployment steps may be followed by health checks.
- Target serialization also prevents an older automatic run from deploying over
  a newer successful run. Manual reruns allow intentional rollback.
- Maintenance admission has a separate bounded connection pool. Image references
  use Docker's registry, namespace and tag defaults. A skipped deploy step does
  not require deployment permission. Historical log caps do not block new runs.
- CPU, memory, PID, workspace and disk reserve limits form an operator-owned host
  profile. Managed deployment permission is described accurately in the UI/docs;
  it is not a boundary around arbitrary external side effects.
- Tokens default to all current and future repositories, with an optional selected
  scope. Repository and ref permissions still apply on every Git request.
- Home and repository Overview have fixed desktop panels and internal scrolling;
  small screens scroll naturally. Mobile cards are more compact. Home operational
  panels cover all readable repositories, independent of list pagination.
- Raw content streams with bounded sniffing: text uses a safe plain-text type,
  raster images keep their native type and other formats download under a sandbox.
- Repository storage is paired with its database by a portable instance marker,
  rather than an absolute path. Documentation and script instructions agree with
  current behavior.

## Validation

Affected Go package suites passed against isolated PostgreSQL schemas. Browser
checks passed for 17 screens, desktop/mobile, both themes, JavaScript on/off,
axe accessibility and keyboard/dialog checks. The Browser runtime was unavailable, so Playwright was
used. Additional browser checks verified unchanged desktop panel sizes as
content grows and all/selected token submission. Snapshot/restore integration passed
with real PostgreSQL and Git, including grants, secrets and retained run history.

Real Docker execution and Compose startup remain unverified because no daemon
is available. The full live-execution browser suite and full race suite were not
run in this pass. No full `make verify` was run, as requested. Source changes and
fixture regressions do not substitute for this remaining operational validation.

The preview uses a separate database and `.data/preview-direction` storage.
It runs at localhost:8080 without a worker; pushed pipelines remain queued.
Nothing has been committed, deployed or published.
