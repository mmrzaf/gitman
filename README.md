# Gitman

Gitman is a lightweight, self-hosted Git hosting service written in Go, designed for simplicity, control, and zero operational overhead.

It runs as a single binary, uses SQLite for metadata, and relies entirely on the system Git and SSH stack. No containers, no external services, no orchestration required.

---

## Core Principles

* **Single Binary** – everything runs inside one compiled executable
* **Zero External Services** – no Redis, no queues, no orchestration layers
* **System-Native** – uses system `git`, `ssh`, and filesystem directly
* **Self-Host First** – optimized for small teams and private infrastructure
* **Automation Ready** – built-in CI/CD without complex pipeline DSLs

---

## Features

### Repository Hosting

* Create and manage Git repositories
* SSH and HTTP(S) Git support
* Private and collaborative repos
* Branch, commit, and file browsing
* Archive downloads (ZIP / tar)

### User & Access Management

* User registration and login
* Session-based authentication
* Personal access tokens (PAT)
* Repository-level permissions
* SSH key management via UI

### Web Interface

* Server-rendered HTML with HTMX
* Minimal, fast, dependency-light UI
* Repository explorer (tree, blob, commits)

### Built-in CI/CD (Gitman CI)

* Script-based pipelines via `.gitman-ci.sh`
* Automatic execution on `git push`
* Isolated worker process
* Artifact storage and retrieval via API
* No YAML, no runners, no external agents

---

## Architecture

```
                  +----------------------+
                  |       Browser        |
                  |     (HTMX + HTML)   |
                  +----------+-----------+
                             |
                             | HTTP
                             v
                  +----------------------+
                  |     gitman web       |
                  |  (API + UI + Git)   |
                  +----------+-----------+
                             |
         +-------------------+-------------------+
         |                                       |
         v                                       v
+----------------------+           +--------------------------+
|     SQLite DB        |           |   Bare Git Repositories  |
| (users/repos/CI/etc) |           |      (filesystem)        |
+----------------------+           +--------------------------+
         |
         v
+----------------------+
|    gitman worker     |
|   (CI execution)     |
+----------------------+
```

### SSH Flow

```
Developer → SSH → system sshd → git user
        → forced command → gitman serve
        → Gitman validates + routes → repo access
```

---

## Technology Stack

* **Language:** Go
* **Router:** Chi
* **Templates:** `html/template`
* **Frontend:** HTMX
* **Database:** SQLite (`modernc.org/sqlite`)
* **Git Integration:** system `git` CLI
* **SSH Integration:** system OpenSSH + forced command

---

## System Components

### `gitman web`

* Web UI and HTTP API
* Git Smart HTTP server
* Auth, repos, tokens, browsing

### `gitman serve`

* SSH entrypoint (forced command)
* Validates key and enforces repo access

### `gitman worker`

* CI/CD executor
* Polls database for jobs
* Runs `.gitman-ci.sh`
* Stores logs and artifacts

---

## CI/CD Overview

Gitman includes a built-in CI/CD system designed to stay minimal and predictable.

### How It Works

1. Developer pushes code
2. `post-receive` hook triggers CI
3. Job is recorded in SQLite
4. `gitman worker` picks up the job
5. Repository is cloned into a temp workspace
6. `.gitman-ci.sh` is executed
7. Logs and artifacts are stored
8. Results become available via API/UI

### Example CI Script

```bash
#!/usr/bin/env bash
set -e

echo "Running tests..."
go test ./...

echo "Building binary..."
go build -o app

mkdir -p artifacts
cp app artifacts/
```

### Environment Variables

Available inside CI:

```
GITMAN_REPO
GITMAN_COMMIT
GITMAN_BRANCH
GITMAN_TAG
GITMAN_EVENT
```

### Artifacts

Stored internally:

```
.data/artifacts/<owner>/<repo>/<run_id>/
```

Accessible via API:

```
GET /api/repos/{owner}/{repo}/artifacts/latest/branch/{branch}/{file}
GET /api/repos/{owner}/{repo}/artifacts/tag/{tag}/{file}
GET /api/repos/{owner}/{repo}/artifacts/commit/{sha}/{file}
```

---

## Repository Model

Repositories are stored as bare Git repos:

```
.data/repos/<owner>/<repo>.git
```

All operations (log, tree, blob, archive) are executed via `git` CLI.

---

## API Overview

### Authentication & Users

| Method | Path        | Description    |
| ------ | ----------- | -------------- |
| GET    | `/login`    | Login page     |
| POST   | `/login`    | Authenticate   |
| GET    | `/register` | Register page  |
| POST   | `/register` | Create account |
| GET    | `/logout`   | Logout         |

### SSH Keys

| Method | Path                | Description |
| ------ | ------------------- | ----------- |
| GET    | `/keys`             | List keys   |
| POST   | `/keys`             | Add key     |
| POST   | `/keys/{id}/delete` | Delete key  |

### Repositories

| Method | Path                 | Description |
| ------ | -------------------- | ----------- |
| GET    | `/repos`             | List repos  |
| POST   | `/repos`             | Create repo |
| POST   | `/repos/{id}/delete` | Delete repo |

### Git (Smart HTTP)

```
/{owner}/{repo}.git/*
```

### CI/CD

| Method | Path                                 | Description      |
| ------ | ------------------------------------ | ---------------- |
| POST   | `/repos/{owner}/{repo}/ci/trigger`   | Trigger pipeline |
| GET    | `/repos/{owner}/{repo}/ci/runs`      | List runs        |
| GET    | `/repos/{owner}/{repo}/ci/runs/{id}` | Run details      |

### Artifacts

| Method | Path                                      |
| ------ | ----------------------------------------- |
| GET    | `/api/repos/{owner}/{repo}/artifacts/...` |

---

## File System Layout

```
.data/
├── db/
│   └── gitman.sqlite
├── repos/
│   └── <owner>/<repo>.git
├── artifacts/
│   └── <owner>/<repo>/<run_id>/
└── authorized_keys
```

---

## Deployment

### Requirements

* Linux server
* `git`
* `openssh-server`

### Setup

1. Create `git` user:

   ```
   useradd -m -s /usr/bin/git-shell git
   ```

2. Build Gitman:

   ```
   go build -o gitman ./cmd/gitman
   ```

3. Run services:

   ```
   ./gitman web
   ./gitman worker
   ```

4. Ensure permissions:

   * `.data/repos` writable by `git`
   * `.data` accessible by Gitman
   * SSH configured to use `authorized_keys`

---

## Security Model

* SSH access restricted via forced command
* No sandboxing for CI (trusted environment assumption)
* Secrets encrypted at rest
* Secure cookies (`HttpOnly`, `SameSite`)
* CSP, HSTS, and security headers enabled
* All Git operations executed with controlled inputs

---

## Design Trade-offs

Gitman intentionally avoids:

* YAML pipeline systems
* Distributed runners
* Kubernetes integrations
* External queues or brokers
* Built-in container orchestration

This keeps the system:

* predictable
* debuggable
* easy to operate

---

## Roadmap

* CI run UI improvements (logs, history)
* Repo-level CI settings
* Artifact browsing UI
* Backup/restore tooling
* Webhook integrations
* Performance optimizations for large repos

---

## Summary

Gitman is not trying to compete with GitHub or GitLab.

It is designed for engineers who want:

* full control
* minimal infrastructure
* predictable behavior
* built-in automation without complexity

If you can run `git` and `ssh`, you can run Gitman.

