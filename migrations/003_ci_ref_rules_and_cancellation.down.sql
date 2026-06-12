-- 003_ci_ref_rules_and_cancellation.down.sql

DROP INDEX IF EXISTS idx_repo_ci_ref_rules_repo_id;
DROP TABLE IF EXISTS repo_ci_ref_rules;

CREATE TABLE ci_runs_rollback_003 (
    id           TEXT PRIMARY KEY,
    repo_id      TEXT NOT NULL,
    commit_hash  TEXT NOT NULL DEFAULT '',
    branch       TEXT NOT NULL DEFAULT '',
    tag          TEXT NOT NULL DEFAULT '',
    event        TEXT NOT NULL DEFAULT 'push',
    status       TEXT NOT NULL DEFAULT 'pending',
    log_file     TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    completed_at INTEGER,
    started_at   INTEGER,
    heartbeat_at INTEGER,
    attempt_id   TEXT NOT NULL DEFAULT '',
    FOREIGN KEY(repo_id) REFERENCES repositories(id) ON DELETE CASCADE
);

INSERT INTO ci_runs_rollback_003 (
    id, repo_id, commit_hash, branch, tag, event, status, log_file,
    created_at, completed_at, started_at, heartbeat_at, attempt_id
)
SELECT
    id, repo_id, commit_hash, branch, tag, event, status, log_file,
    created_at, completed_at, started_at, heartbeat_at, attempt_id
FROM ci_runs;

DROP TABLE ci_runs;
ALTER TABLE ci_runs_rollback_003 RENAME TO ci_runs;

CREATE INDEX idx_ci_runs_repo_id ON ci_runs(repo_id);
CREATE INDEX idx_ci_runs_status ON ci_runs(status);
CREATE INDEX idx_ci_runs_branch ON ci_runs(branch);
CREATE INDEX idx_ci_runs_tag ON ci_runs(tag);
CREATE INDEX idx_ci_runs_commit_hash ON ci_runs(commit_hash);
CREATE INDEX idx_ci_runs_created_at ON ci_runs(created_at);
CREATE INDEX idx_ci_runs_heartbeat_at ON ci_runs(heartbeat_at);
CREATE INDEX idx_ci_runs_attempt_id ON ci_runs(attempt_id);
