-- 005_ci_trigger_idempotency.up.sql
-- Stable delivery keys let the durable hook queue retry without duplicate runs.

ALTER TABLE ci_runs ADD COLUMN trigger_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_ci_runs_trigger_key
    ON ci_runs(trigger_key)
    WHERE trigger_key <> '';
