# CI artifacts

A job publishes artifacts by writing regular files under `/gitman/artifacts`.

```yaml
image: alpine:3.24
steps:
  - name: build
    run: |
      mkdir -p /gitman/artifacts/reports
      printf 'ok\n' > /gitman/artifacts/reports/status.txt
```

Nested files are supported. Symlinks and non-regular files are skipped.

## Collection behavior

Artifacts are collected after the container exits, including after a failed job. Repository members can download artifacts from the run page. Run-specific API downloads can also retrieve artifacts from failed runs when the caller knows the run ID.

Branch-, tag-, and commit-based API lookups resolve only successful runs.

## Default limits

| Limit | Default |
| --- | --- |
| Artifact bytes per run | 100 MiB |
| Artifact files per run | 1,000 |
| CI log bytes per run | 10 MiB |

Artifact staging and workspace checks are application-level safeguards, not kernel-enforced quotas.

## API downloads

Use a personal access token as a bearer token. See the [artifact HTTP API reference](../reference/artifact-http-api.md).
