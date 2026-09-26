# Git transports

Gitman supports Git over HTTPS only — the smart HTTP protocol
(`git-upload-pack`/`git-receive-pack`), served by the `web` process. There
is no SSH transport; this is a deliberate scope decision (see
[Not added, on purpose](../README.md#not-added-on-purpose)), not a gap.

## Authentication

Use your Gitman username and an access token (from **Access tokens**, or
`gitman admin token create`) as the HTTP password:

```sh
git clone https://git.example.com/waiotech.git
Username: darius
Password: <access token>
```

Most Git clients and credential helpers cache this the same way they
would a personal access token on any other Git host.

- A **read** token can clone and fetch.
- A **write** token can also push.

## Push behavior

A push is rejected before anything is written if a matching ref rule
forbids it (who may push, whether force-push or deletion is allowed). If
the ref's rule allows running the pipeline, `web`'s `post-receive` hook
queues a run and the push output prints a link to it.

## Large clones and pushes

If Gitman sits behind a reverse proxy (see
[Docker deployment](operator/docker.md)), a large clone or push can
outlast the proxy's own read timeout before Gitman's smart-HTTP handler
finishes. Raise the proxy's timeout for Gitman's route rather than
Gitman's own configuration, which has no separate knob for this.
