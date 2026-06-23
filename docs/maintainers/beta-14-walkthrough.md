# Beta 14 walkthrough

Use this walkthrough after applying the beta 14 hardening changes and before tagging `v1.0.0-beta.14`.

## 1. Source sanity

```bash
git status
git grep -n 'v1.0.0-beta.12\|v1.0.0-beta.13' -- . ':!internal/ci/*_test.go'
gofmt -w internal/handlers/app.go internal/worker/worker.go
gofmt -l $(git ls-files '*.go')
```

Expected:

- No runtime state in the tree: `.data/`, `data/`, `*.sqlite`, `bin/`, `dist/`.
- `static/`, `templates/`, and `migrations/` are present in the tracked source archive.
- Go files are formatted.

## 2. CI ref rules

Create these rules on each Docker-building repository:

| Type | Ref or pattern | Auto-run | Secrets | Docker socket |
| --- | --- | ---: | ---: | ---: |
| branch | `main` | yes | as needed | yes when `.gitman-ci.yml` has `docker: true` |
| branch | `develop` | optional | as needed | yes when `.gitman-ci.yml` has `docker: true` |
| tag | `v*` | yes | as needed | yes when release tags build Docker images |

Confirm that a missing Docker rule now fails with a clear log message and fix hint.

## 3. Gitman CI smoke tests

Run one manual CI job for each case:

- default branch success
- non-default branch success with a rule
- release tag success through `tag v*`
- Docker socket denied on an untrusted ref
- secret denied on an untrusted ref
- failing shell step
- missing `.gitman-ci.yml` skipped run
- artifact creation and download
- full log download

Expected log quality:

- CI header includes repository, commit, branch/tag, image, step count, policy, timeout, and workspace.
- Each step has start and success/failure markers.
- Pre-container failures show `ERROR`, `Details`, and `Fix` lines.
- Git detached-head advice does not appear.

## 4. UI pass

Open these pages in light and dark mode:

- `/repos`
- repository file browser
- CI/CD run list
- CI run detail
- CI secrets
- collaborators
- tokens
- SSH keys

Expected UI behavior:

- Status badges are consistent across run list and run detail.
- CI rules table uses the same neutral spacing as the rest of the app.
- Primary, secondary, and danger buttons are visually distinct but not flashy.
- Long hashes/log lines do not break the page layout.
- Mobile width does not hide CI actions.

## 5. Docker image build

```bash
VERSION=v1.0.0-beta.14 docker build \
  --build-arg VERSION=v1.0.0-beta.14 \
  --build-arg GO_IMAGE=golang:1.26-bookworm \
  --build-arg RUNTIME_IMAGE=debian:bookworm-slim \
  --build-arg GOPROXY=https://mirror.abrha.net/repository/go/,direct \
  -t gitman:v1.0.0-beta.14 .

docker run --rm gitman:v1.0.0-beta.14 gitman version
```

Expected output:

```text
v1.0.0-beta.14
```

## 6. Release gates

```bash
VERSION=v1.0.0-beta.14 make verify
VERSION=v1.0.0-beta.14 make release-source
tar -tzf dist/gitman-1.0.0-beta.14.tar.gz | sort
```

The archive must include:

```text
migrations/
templates/
static/
```

The archive must not include:

```text
.git/
.data/
data/
*.sqlite
bin/
dist/
.env
```

## 7. Tag and run release

```bash
git tag -a v1.0.0-beta.14 -m "Gitman v1.0.0-beta.14"
git push origin develop
git push origin v1.0.0-beta.14
```

After the release workflow finishes, verify:

- GitHub release assets are present.
- Source archive checksum exists.
- Docker image has the `v1.0.0-beta.14` tag.
- `gitman version` inside the published image returns `v1.0.0-beta.14`.
