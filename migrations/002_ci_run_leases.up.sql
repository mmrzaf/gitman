-- 002_ci_run_leases.up.sql
-- Add attempt-scoped worker leases. Every claim receives a new attempt ID so a
-- stale worker cannot heartbeat or complete a replacement execution.

ALTER TABLE ci_runs ADD COLUMN started_at INTEGER;
ALTER TABLE ci_runs ADD COLUMN heartbeat_at INTEGER;
ALTER TABLE ci_runs ADD COLUMN attempt_id TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_ci_runs_heartbeat_at ON ci_runs(heartbeat_at);
CREATE INDEX idx_ci_runs_attempt_id ON ci_runs(attempt_id);
