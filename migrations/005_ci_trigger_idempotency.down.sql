-- 005_ci_trigger_idempotency.down.sql

DROP INDEX IF EXISTS idx_ci_runs_trigger_key;
ALTER TABLE ci_runs DROP COLUMN trigger_key;
