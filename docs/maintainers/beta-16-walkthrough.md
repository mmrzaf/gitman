# Beta 16 walkthrough

Use this walkthrough after merging the Beta 16 usability work and before tagging `v1.0.0-beta.16`.

## Upgrade and rollback

Beta 16 adds no database migration. Back up the SQLite database, repositories, artifacts, CI cache, and generated `authorized_keys` before upgrading anyway so rollback restores one coherent snapshot.

To roll back, stop the web and worker processes, restore the pre-upgrade database and matching filesystem snapshot, then start the previous Gitman binaries. Do not mix binaries and filesystem/database snapshots from different points in time during a rollback drill.

## Automated verification

Run the release gate with the Go version declared in `go.mod` and Docker available:

```bash
VERSION=v1.0.0-beta.16 make verify
```

The gate must complete tests, vet, lint, vulnerability scanning, binary version injection, and the Docker smoke build.

## Repository experience

Exercise a public repository anonymously and a private repository as owner, read collaborator, write collaborator, and unrelated user.

Verify:

- The repository masthead, active navigation, description, visibility, current ref, clone controls, and archive controls are coherent on Files, Commits, CI/CD, Collaborators, Secrets, and Settings.
- Repository names such as `ci`, `commits`, and `settings` do not confuse the active navigation tab.
- Branch, tag, and detached-commit browsing preserve the selected source context when moving between Files and Commits.
- Relative timestamps have useful absolute timestamps on hover/title.
- Copy controls report success only when the clipboard write succeeds.
- `t`, `g f`, `g c`, `g i`, and `?` work outside editable fields; the shortcut dialog documents the available keys.
- Go to File works with keyboard-only navigation and odd valid filenames, and never offers gitlinks/submodules as blobs.
- With JavaScript disabled, repository navigation, ref selection through form/page links, source browsing, commits, CI pages, clone/archive controls, and destructive forms remain usable except for explicitly progressive features such as live-log updates, copy buttons, and keyboard shortcuts.

## Repository home and README

Verify a root README containing headings, repeated headings, paragraphs, lists, blockquotes, fenced code, inline code, emphasis, repository-relative links, external HTTP(S) links, and hostile raw HTML.

Confirm:

- Repeated headings receive unique anchors.
- Raw HTML and dangerous URL schemes are not interpreted.
- Relative repository links remain inside the current repository/ref context.
- Large READMEs degrade to an ordinary file link instead of rendering unbounded content.
- Empty repositories still show correct SSH and HTTP setup commands.

## Commits, diffs, and source

Exercise normal, root, merge, rename, delete, binary, and large commits.

Confirm:

- Commit pages show immutable commit identity, parents, refs, file statistics, CI status, and first-parent merge diffs.
- Unified diff line numbers link to the exact immutable source revision where applicable.
- Added, deleted, renamed, and binary files render without path corruption.
- File paths containing spaces, tabs, Unicode, and newlines survive Git inspection safely.
- Source pages show breadcrumbs, language, size, line numbers, syntax highlighting, Raw, Download, Copy file, and immutable Permalink actions.
- `#L12` and `#L12-L30` selections work and a permalink retains the selected range.
- Huge line ranges in the URL do not freeze the browser.
- Files over the byte, line-count, or single-line interactive rendering limits degrade to Raw/Download instead of creating pathological DOM output.
- Raw repository content is served as `text/plain`; downloads preserve exact blob bytes.

## CI experience

Exercise queued, running, successful, failed, cancelled, skipped, retried, and infrastructure-failure runs.

Confirm:

- The run list shows commit subject/ref/status/timing and status/branch filters preserve URL state.
- A run page exposes Setup, named steps, Finalize, step status/duration/exit code, raw logs, pipeline configuration, retry lineage, and artifacts.
- Successful steps collapse appropriately; the running or failed step is easy to find.
- Follow mode does not yank the viewport after the user scrolls up, and the new-output indicator returns to the live tail.
- Raw-log search, wrap, raw/structured views, copy, and download behave correctly on large logs.
- Search reports `300+` rather than pretending more than 300 highlighted matches are individually navigable.
- Incremental log polling appends from byte offsets and preserves multibyte UTF-8 characters when a poll lands in the middle of a rune.
- A log truncation/reset event recovers cleanly.
- Retry lineage stays attached to the exact commit.
- Running a historical commit preserves a branch trust context only when that commit is still reachable from the branch. Tag trust context is carried only by the commit currently referenced by that tag; older tag-history commits run detached. In all cases, the checked-out commit remains immutable and exact.
- Artifact trees preserve nested paths; safe text previews work; binary and oversized artifacts download normally.

## Browser and accessibility pass

Check a narrow phone-sized viewport, a desktop viewport, keyboard-only navigation, and light/dark system themes.

Confirm:

- No page-level horizontal overflow is introduced by repository navigation, source, diffs, logs, or action groups.
- Focus indicators remain visible.
- Dialogs have labels, Escape closes them, and clicking the backdrop closes them when JavaScript enhancement is active.
- The Go to File combobox exposes its listbox and active option to assistive technology.
- Source/diff/log content scrolls inside its intended region rather than forcing the whole page wider.

## Security-header smoke test

Inspect a representative HTML response and confirm the Content Security Policy does not require inline script or inline style execution:

```text
script-src 'self'; style-src 'self'
```

Check that repository-controlled README, commit, ref, path, diff, source, and log strings cannot inject executable HTML.

## Package and verify source

From the clean tagged Git checkout:

```bash
VERSION=v1.0.0-beta.16 make release-source
tar -tzf dist/gitman-1.0.0-beta.16.tar.gz | sort
sha256sum -c dist/gitman-1.0.0-beta.16.tar.gz.sha256
```

The source archive must include `migrations/`, `templates/`, and `static/` and must not include `.git`, `.env`, runtime data, databases, generated `authorized_keys`, CI logs/artifacts, binaries, or build output.

Verify release version injection:

```bash
go build -trimpath -ldflags "-s -w -X main.version=v1.0.0-beta.16" -o bin/gitman ./cmd/gitman
test "$(bin/gitman version)" = "v1.0.0-beta.16"
test "$(bin/gitman --version)" = "v1.0.0-beta.16"
```

## Tag

Only after the automated gate and walkthrough are green:

```bash
git tag -a v1.0.0-beta.16 -m "Gitman v1.0.0-beta.16"
git push origin v1.0.0-beta.16
```
