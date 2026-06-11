# CI secrets

Repository owners manage CI secrets from the repository **Secrets** page. Secret storage is disabled until the operator configures `GITMAN_SECRET_KEY` on both web and worker processes.

## Reference a secret

Store a key such as `DEPLOY_TOKEN`, then reference it as an environment variable:

```yaml
env:
  DEPLOY_TOKEN: ${{ secrets.DEPLOY_TOKEN }}
```

Secret keys must start with an uppercase letter and contain only uppercase letters, digits, and underscores.

## Trust boundary

A repository writer can change `.gitman-ci.yml`, run shell commands, and exfiltrate any secret injected into that repository's jobs. Only store a secret when every user with write access is trusted with the plaintext value.

## Encryption and recovery

Gitman encrypts stored values at rest before saving them in SQLite. `GITMAN_SECRET_KEY` is deployment state and is not stored in Gitman backups. Preserve it in an external secret manager. Losing or changing it makes existing stored values unreadable until the original key is restored.

## Log masking is defense in depth

The worker masks exact configured plaintext secret values in logs. This is not a complete data-loss prevention mechanism. Encoded, transformed, split, truncated, or indirectly transmitted values may still escape masking. Jobs must not print secrets.

## Value limitations

Secrets are injected through an environment file. Values containing NUL, carriage-return, or newline characters are not supported.
