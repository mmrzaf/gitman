-- 004_ci_run_controls.up.sql
-- User-visible run outcomes and retry lineage for CI controls.

ALTER TABLE ci_runs ADD COLUMN status_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE ci_runs ADD COLUMN retry_of_run_id TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_ci_runs_retry_of_run_id ON ci_runs(retry_of_run_id);
