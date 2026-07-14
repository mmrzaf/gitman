# Beta 15 walkthrough

Use this walkthrough after applying the Beta 15 changes and before tagging `v1.0.0-beta.15`.

## 1. Upgrade and rollback boundary

Back up the SQLite database, repositories, artifacts, CI cache, and generated `authorized_keys` before starting the new binary. Beta 15 applies migrations 004 and 005 at startup:

- migration 004 adds user-visible CI outcome reasons and retry lineage;
- migration 005 adds a unique, non-empty durable-trigger key to prevent duplicate runs after delivery retries.

Both migrations are additive. Do not run a Beta 14 process against a database after Beta 15 has started. To roll back, stop all Gitman processes, restore the pre-upgrade backup, restore the matching filesystem snapshot, and then start the Beta 14 binaries. The down migrations exist for development, but backup restore is the production rollback path.

Managed hooks no longer use `GITMAN_INTERNAL_URL`; remove it from the deployment when convenient. Beta 15 ignores the variable if it remains set.

## 2. Automated source gate

```bash
git status --short
gofmt -l $(git ls-files '*.go')
git diff --check
GOCACHE=/tmp/gitman-go-cache go test ./...
GOCACHE=/tmp/gitman-go-cache go test -race ./...
GOCACHE=/tmp/gitman-go-cache go vet ./...
VERSION=v1.0.0-beta.15 make verify
```

`make verify` also requires Go 1.26, `golangci-lint`, and Docker; it runs the pinned `govulncheck` version through Go. The release workflow repeats formatting, race tests, vet, lint, vulnerability scanning, native version checks, and a Docker version smoke test from the release tag.

## 3. Startup, probes, and configuration

Start the web process with a copy of production configuration. Confirm invalid explicit booleans, sizes, durations, URLs, log levels, CPU and memory limits, path mappings, and an out-of-range port stop startup with the relevant environment-variable name.

```bash
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
curl -i http://localhost:8080/health
```

Expected:

- all healthy responses are JSON, `200`, and `Cache-Control: no-store`;
- probes do not emit session or CSRF cookies;
- `/healthz` remains healthy if SQLite is unavailable;
- `/readyz` and `/health` return `503` with only the failed component name, not a filesystem path.

## 4. Authentication and SSH keys

- Request an authenticated browser page near the session-extension threshold and confirm the response refreshes the session cookie.
- Request an unauthenticated `/api/repos/...` artifact URL and confirm a JSON `401`, not an HTML redirect.
- Add a valid RSA, ECDSA, and Ed25519 public key and confirm the displayed canonical key and SHA-256 fingerprint.
- Confirm malformed keys and duplicate key material are rejected even when comments differ.
- Restart the web process and confirm the managed `authorized_keys` file is synchronized atomically from SQLite.
- Simulate an unwritable `authorized_keys` destination and confirm key add/delete rolls back rather than diverging from SQLite.

## 5. Durable CI triggers

Install or upgrade the managed hook from the repository CI page. Confirm the hook contains no secret, writes only to `hooks/gitman-ci-queue`, and revokes the obsolete Beta 14 managed-hook webhook secret.

1. Stop the web process while leaving Git receive available.
2. Push several branch updates and an annotated tag.
3. Delete or move the tag before restarting the web process.
4. Restart the web process.

Expected:

- the Git push succeeds while the web process is stopped;
- accepted event filenames contain a fixed-width monotonic sequence, even when file timestamps are forced to the same value;
- every trusted event is drained to SQLite once and in original push order, including the annotated tag's peeled commit;
- delivery retries do not duplicate a run;
- a newer pending push for the same exact ref cancels the older pending push with a visible reason and remains the effective pending run;
- an interrupted `.processing-event-*` claim is recovered immediately after web restart before later sequence numbers;
- a temporarily unresolvable object remains queued and prevents later events in that repository from overtaking it;
- malformed events are isolated as `.malformed-event-*` and reported without blocking later valid events.

## 6. CI controls and worker lifecycle

Exercise successful, failed, skipped, and timed-out jobs. Confirm queue time, runtime, exit outcome, concise reason, retry ancestry, artifacts, downloadable logs, and live-log updates.

- Cancel a pending job: it must never be claimed.
- Cancel a running job: the container must stop, the old attempt must not overwrite `cancelled`, and repository deletion must remain blocked until the worker acknowledges shutdown.
- Retry every terminal status: the new pending run must target the exact original commit and leave the source run unchanged.
- Send one worker shutdown signal during a job: polling stops and the job drains.
- Let the grace period expire or send a second signal: the active job is cancelled and the worker exits after cleanup.
- Kill a worker during a run and confirm lease reconciliation removes managed stale containers and safely requeues the attempt.

## 7. Repository settings and deletion

- As an owner, update description and public/private visibility from **Settings**.
- As a collaborator, confirm the settings page returns `403`.
- Attempt deletion with pending, running, and still-stopping cancelled CI; each must be refused.
- Leave a durable push event queued (or temporarily unresolvable) and confirm deletion is refused until it is safely drained.
- Delete an idle repository and confirm its bare repository is quarantined before the database delete, then its logs, artifacts, and cache are removed.
- Force the database delete to fail and confirm the quarantined repository is restored.
- Race a worker claim against deletion and confirm the atomic database guard restores the repository instead of orphaning the attempt.

## 8. UI pass

Inspect repository settings, the CI run list, and CI run details in light mode, dark mode, and a narrow mobile viewport. Status badges and action buttons must stay readable, long refs and logs must not overflow, dangerous actions must remain visually distinct, and terminal pages must stop live polling.

## 9. Release artifacts

```bash
VERSION=v1.0.0-beta.15 make release-source
tar -tzf dist/gitman-1.0.0-beta.15.tar.gz | sort
sha256sum -c dist/gitman-1.0.0-beta.15.tar.gz.sha256

go build -trimpath -ldflags "-s -w -X main.version=v1.0.0-beta.15" -o bin/gitman ./cmd/gitman
test "$(bin/gitman version)" = "v1.0.0-beta.15"
test "$(bin/gitman --version)" = "v1.0.0-beta.15"
```

The source archive must contain `migrations/`, `templates/`, and `static/`, and must contain no runtime data, database, credentials, generated `authorized_keys`, coverage output, `bin/`, or `dist/`.

Build and run the Docker image with the release version, then verify all four supported Linux and macOS binaries created by the release workflow. Only tag after every automated gate and applicable manual check passes:

```bash
git tag -a v1.0.0-beta.15 -m "Gitman v1.0.0-beta.15"
git push origin develop
git push origin v1.0.0-beta.15
```
