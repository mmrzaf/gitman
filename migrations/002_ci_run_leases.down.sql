-- 002_ci_run_leases.down.sql

DROP INDEX IF EXISTS idx_ci_runs_attempt_id;
DROP INDEX IF EXISTS idx_ci_runs_heartbeat_at;

ALTER TABLE ci_runs DROP COLUMN attempt_id;
ALTER TABLE ci_runs DROP COLUMN heartbeat_at;
ALTER TABLE ci_runs DROP COLUMN started_at;
