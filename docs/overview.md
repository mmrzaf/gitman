# Overview

Gitman is a self-hosted Git server with built-in pipelines, for one person
or a small team running it on their own server. It provides:

- Git over HTTPS, with per-person access tokens as the only credential.
- A web interface for browsing code, diffs, commits and pipeline runs.
- Pipelines that build and ship with Docker on the same host the worker
  runs on.

It deliberately stays small: one binary (`gitman`), one PostgreSQL
database, and a handful of ideas — people, repositories, ref rules, and
pipelines. See [Not added, on purpose](../README.md#not-added-on-purpose)
in the top-level README for what it explicitly leaves out (SSH, pull
requests/issues/wikis, organizations, anonymous access, image pulling,
CI artifacts/caches, webhooks, and more).

For how the pieces run together, see [Architecture](architecture.md). To
get an instance running, see [Getting started](getting-started.md).
