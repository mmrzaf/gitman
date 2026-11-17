# Release checklist

A stable release must be tested on a fresh installation and as a restore from a compatible backup.
Release publishing is automated by [`.github/workflows/release.yml`](../../.github/workflows/release.yml)
for existing `v*` tags. Do not tag a commit until its full acceptance suite
is green and the operator pilot is complete.


## Prepare the candidate

1. Freeze features; review the diff, security, third-party licenses and the
   [verification matrix](verification-matrix.md). Use a **disposable** database
   and data directory for every test. Keep production backups and the secret key
   separately; never point integration scripts at a production instance.
2. Update [`CHANGELOG.md`](../../CHANGELOG.md) with a dated entry for the
   release being tagged, and update the relevant user-facing entry in
   [`internal/web/releasenotes.json`](../../internal/web/releasenotes.json).
   Check the content against the actual shipped behavior; do not mark unrun
   tests as passed.
3. Run `make verify` with **Go 1.27, Node 24, ShellCheck, PostgreSQL 18** and
   `GITMAN_TEST_DATABASE_URL` pointing to a dedicated empty `_test` database.
   CI requires ShellCheck (it does not silently skip it).
4. Run the complete host integration suite on a separate test system. See
   [`scripts/README.md`](../../scripts/README.md): `clean-install-test.sh`,
   `backup-test.sh`, `e2e.sh`, `multiworker.sh`, `compose-smoke.sh`,
   `browser-smoke.sh`, and `browser.sh`. Docker must have the required images.
5. On a separate clean host/VM, rehearse a new installation and a restore from
   its own matching-version backup. Verify Git history, access rules, secret decryption,
   logs and deployments. Test host power/network/database interruption and
   multiworker coordination across independent Docker hosts. These operational
   checks are not replaced by same-host CI fixtures.
6. Push representative pipeline configurations only into an isolated test
   instance; do not execute production deploy scripts or alter live services.

## Publish

```sh
git tag v1.0.0
git push origin v1.0.0
```

The tagged release workflow is fail-closed and proceeds in this order:

1. **Verify** source, security checks, race tests, Docker/Git integration,
   backup and restore, two workers, the production Compose smoke install, and
   full Chromium/accessibility acceptance.
2. **Build** version-stamped Linux `amd64` and `arm64` executables, a clean
   source tarball, and a loadable `amd64` Docker archive.
3. **Validate artifacts**: exact file set, complete SHA-256 checksums, source
   archive hygiene, executable version, and load/run the offline Docker image.
4. **Stage a draft GitHub Release** with verified assets and changelog notes.
   Drafts are intentionally not announced as released. Rerunning a failed
   draft workflow may replace its draft assets; do not restage a published tag.
5. **Publish only the immutable versioned GHCR image** for both platforms,
   then inspect its multi-arch manifest.
6. **Publish the existing draft GitHub Release**, only after the image exists.
7. **Promote floating GHCR tags** (`major.minor` and `latest`) from the exact
   versioned manifest for non-prereleases, without rebuilding, **only if this
   is still the newest published stable release**. A promotion
   failure is visible in the workflow and can be retried; the immutable
   version tag remains authoritative.

No release workflow can make GitHub Releases and GHCR updates a single atomic
transaction. Staging first minimizes partial publication, but interruption may
leave a **draft** with assets or a versioned image awaiting final publication.
Never overwrite an existing published release without an explicit operator
review. Prefer exact image version tags for deployment pinning.

To retry publishing a **draft**, dispatch the workflow with the existing tag.
Do not move or recreate a release tag.

## Post-publication checks

- Verify the published release is not a draft and includes four distributables
  and `SHA256SUMS`. Download and run
  `sha256sum -c --ignore-missing SHA256SUMS`.
- On a clean host, run `gitman version` from the download and load the offline
  Docker archive. Check `docker run --rm ghcr.io/<repo>:1.0.0 gitman version`.
- Confirm GHCR manifests for `linux/amd64` and `linux/arm64`; for stable releases,
  confirm `latest` and the major/minor tag point to the same content.
- Verify fresh installation, Git over HTTPS, web readiness, workers, a safe
  pipeline, backup and restore. Confirm release notes and version display.
- Record the evidence, limitations and go/no-go decision. Do not claim 1.0.0 is
  production-ready while an external host acceptance gate remains untested.

## Known design boundaries

- A clean installation must initialize the schema and support core operations.
  Applied migrations are immutable after publication.
- Worker-loss recovery never blindly repeats potentially executed deployments.
- Deployment script exit status is the deployment outcome; applications that
  need service-health verification should put that check in their deploy script.
- Gitman workers have privileged access to their Docker host; the supported
  execution model trusts the operator and pipeline authors.
