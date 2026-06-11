# Product overview

## What Gitman is

Gitman is an opinionated small-team Git forge. Metadata is stored in SQLite. Bare Git repositories are stored on disk. Git transport operations are delegated to the system `git` executable. The web UI, database migrations, templates, and static assets are embedded into the Go binary.

The product has two independently runnable processes:

| Process | Purpose | Required dependencies |
| --- | --- | --- |
| `gitman web` | Browser UI, Git smart HTTP, CI trigger webhook, health endpoint | `git` |
| `gitman worker` | Polls queued CI runs and starts restricted Docker containers | `git`, Docker CLI, access to Docker daemon |

Optional SSH Git transport uses the host OpenSSH server and a host-visible `gitman` wrapper. It is not implemented by an embedded SSH daemon.

## Access model

A repository has one owner and can have collaborators:

| Role | Browse private source | Pull | Push | View CI logs and artifacts | Run CI manually | Manage collaborators | Manage CI secrets and hook |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Owner | Yes | Yes | Yes | Yes | Yes | Yes | Yes |
| `write` collaborator | Yes | Yes | Yes | Yes | Yes | No | No |
| `read` collaborator | Yes | Yes | No | Yes | No | No | No |
| Anonymous visitor to public repo | Yes | Yes over HTTP | No | No | No | No | No |

Public repository source browsing does not make CI logs or artifacts public.

## CI trust model

The CI worker executes repository-controlled shell commands in Docker containers. Gitman applies useful restrictions: no network by default, read-only container root filesystem, dropped Linux capabilities, `no-new-privileges`, PID limits, CPU and memory limits, numeric non-root user enforcement, bounded logs, bounded artifact staging, bounded workspace usage, and serialized per-repository cache writes.

Those controls reduce risk. They do not turn a Docker-socket-backed worker into a hardened multi-tenant sandbox. Operate it as privileged infrastructure.

## Storage model

Default source-mode paths are relative to the working directory:

```text
.data/
├── db/gitman.sqlite
├── repos/<owner>/<repo>.git
├── authorized_keys
├── artifacts/
│   ├── logs/<owner>/<repo>/<run-id>/<attempt-id>.log
│   └── files/<owner>/<repo>/<run-id>/<attempt-id>/...
└── ci/
    ├── cache/<owner>/<repo>/current
    └── workspaces/...
```

The Docker Compose deployment maps the same data under `/data` inside the containers.
