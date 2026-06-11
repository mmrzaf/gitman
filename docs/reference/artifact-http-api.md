# Artifact HTTP API

Artifact downloads require repository membership and authentication. Use a personal access token:

```bash
TOKEN='gm_...'
```

Nested artifact paths are supported.

## Latest successful run for a branch

```bash
curl -fL \
  -H "Authorization: Bearer $TOKEN" \
  'https://git.example.com/api/repos/alice/project/artifacts/latest/branch/reports/status.txt?ref=main' \
  -o status.txt
```

## Successful run for a tag

```bash
curl -fL \
  -H "Authorization: Bearer $TOKEN" \
  'https://git.example.com/api/repos/alice/project/artifacts/tag/dist/project.tar.gz?ref=v1.0.0' \
  -o project.tar.gz
```

## Successful run for a commit

```bash
curl -fL \
  -H "Authorization: Bearer $TOKEN" \
  'https://git.example.com/api/repos/alice/project/artifacts/commit/<commit-hash>/reports/status.txt' \
  -o status.txt
```

## Specific run

```bash
curl -fL \
  -H "Authorization: Bearer $TOKEN" \
  'https://git.example.com/api/repos/alice/project/artifacts/run/<run-id>/reports/status.txt' \
  -o status.txt
```

Run-specific downloads can retrieve artifacts collected from a failed run. Branch-, tag-, and commit-based lookups select successful runs only.
