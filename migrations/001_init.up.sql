-- 001_init.up.sql
-- Gitman database schema – version 1 (complete)

-- Users
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    username      TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Sessions (one per browser)
CREATE TABLE sessions (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    expires_at INTEGER NOT NULL, -- Unix timestamp
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Repositories
CREATE TABLE repositories (
    id              TEXT PRIMARY KEY,
    owner_id        TEXT NOT NULL,
    name            TEXT NOT NULL,
    description     TEXT,
    webhook_secret  TEXT NOT NULL DEFAULT '',
    is_private      BOOLEAN NOT NULL DEFAULT 0,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(owner_id, name),
    FOREIGN KEY(owner_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Collaborators (access control)
CREATE TABLE repo_collaborators (
    repo_id      TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    access_level TEXT NOT NULL CHECK(access_level IN ('read', 'write')),
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (repo_id, user_id),
    FOREIGN KEY(repo_id) REFERENCES repositories(id) ON DELETE CASCADE,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- Personal access tokens (for HTTP Basic Auth)
CREATE TABLE access_tokens (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    name       TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- SSH keys (for git‑shell forced commands)
CREATE TABLE ssh_keys (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    name       TEXT NOT NULL,
    public_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- CI runs (pipeline executions)
CREATE TABLE ci_runs (
    id           TEXT PRIMARY KEY,
    repo_id      TEXT NOT NULL,
    commit_hash  TEXT NOT NULL DEFAULT '',
    branch       TEXT NOT NULL DEFAULT '',
    tag          TEXT NOT NULL DEFAULT '',
    event        TEXT NOT NULL DEFAULT 'push',
    status       TEXT NOT NULL DEFAULT 'pending',
    log_file     TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    FOREIGN KEY(repo_id) REFERENCES repositories(id) ON DELETE CASCADE
);

-- Repository secrets (encrypted)
CREATE TABLE repo_secrets (
    id              TEXT PRIMARY KEY,
    repo_id         TEXT NOT NULL,
    key             TEXT NOT NULL,
    encrypted_value TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(repo_id, key),
    FOREIGN KEY(repo_id) REFERENCES repositories(id) ON DELETE CASCADE
);

-- ============================================================
-- Indexes (for performance)
-- ============================================================

-- Users
CREATE INDEX idx_users_username ON users(username);

-- Repositories
CREATE INDEX idx_repositories_owner_id ON repositories(owner_id);

-- Access tokens
CREATE INDEX idx_access_tokens_token_hash ON access_tokens(token_hash);

-- SSH keys
CREATE INDEX idx_ssh_keys_user_id ON ssh_keys(user_id);

-- CI runs
CREATE INDEX idx_ci_runs_repo_id ON ci_runs(repo_id);
CREATE INDEX idx_ci_runs_status ON ci_runs(status);
CREATE INDEX idx_ci_runs_branch ON ci_runs(branch);
CREATE INDEX idx_ci_runs_tag ON ci_runs(tag);
CREATE INDEX idx_ci_runs_commit_hash ON ci_runs(commit_hash);
CREATE INDEX idx_ci_runs_created_at ON ci_runs(created_at);

-- Webhook secret lookup (used by the webhook auth middleware)
CREATE INDEX idx_repositories_webhook_secret ON repositories(webhook_secret);

-- Repo secrets
CREATE INDEX idx_repo_secrets_repo_id ON repo_secrets(repo_id);
