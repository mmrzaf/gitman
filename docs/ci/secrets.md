# Secrets

Repository secrets are encrypted at rest with `GITMAN_SECRET_KEY`. If
that variable is empty, secret storage is disabled instance-wide — there
is no per-repository way to turn it on regardless of key.

## Setting a key

`GITMAN_SECRET_KEY` must be base64 encoding of exactly 32 random bytes:

```sh
openssl rand -base64 32
```

Changing the key after secrets have been stored makes them unreadable —
treat it like any other encryption key, not a rotatable password.

## Who can add secrets

Only an admin can add or change a repository's secrets: the **Settings**
page is gated by the global admin role, with no other way to reach it.

## Whether a run gets them

A ref rule must explicitly allow secrets (`--secrets` on
`gitman admin rule set`) for a run on that ref to receive them. A ref no
rule matches gets no secrets, the same as it gets no Docker access.

## How they reach a step

- Secrets are injected as environment variables into every step of a run
  that's allowed to receive them.
- They never appear on a command line.
- Their values are masked wherever run output is stored and displayed.

See [Security model](../operator/security.md) for how this fits with the
rest of the ref-rule permission system.

Secret values must contain valid UTF-8, contain no NUL bytes, and occupy at
most 8 KiB each or 128 KiB in total per repository. Redaction matches bytes
before text normalization and preserves matches across arbitrary writes,
newlines, and storage chunks. Complete values are always masked; individual
lines of a multiline value are additionally masked when at least four bytes.
Keep the encryption key in a separate protected backup; losing it makes
stored secrets unrecoverable.
