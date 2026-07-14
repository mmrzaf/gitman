-- 004_ci_run_controls.down.sql

DROP INDEX IF EXISTS idx_ci_runs_retry_of_run_id;
ALTER TABLE ci_runs DROP COLUMN retry_of_run_id;
ALTER TABLE ci_runs DROP COLUMN status_reason;
