-- 003_ci_ref_rules_and_cancellation.up.sql
-- Exact per-repository CI ref trust rules and visible run cancellation reasons.

ALTER TABLE ci_runs ADD COLUMN cancel_reason TEXT NOT NULL DEFAULT '';

CREATE TABLE repo_ci_ref_rules (
    repo_id             TEXT NOT NULL,
    ref_type            TEXT NOT NULL CHECK(ref_type IN ('branch', 'tag')),
    ref_name            TEXT NOT NULL,
    auto_run            BOOLEAN NOT NULL DEFAULT 0,
    allow_secrets       BOOLEAN NOT NULL DEFAULT 0,
    allow_docker_socket BOOLEAN NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    updated_at          INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    PRIMARY KEY (repo_id, ref_type, ref_name),
    FOREIGN KEY(repo_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE INDEX idx_repo_ci_ref_rules_repo_id
    ON repo_ci_ref_rules(repo_id);
