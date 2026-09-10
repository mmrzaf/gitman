-- 012_ci_queue_index.up.sql
-- CI workers claim the oldest pending run, and operator status reports queue
-- depth/age. A composite index keeps both operations efficient as history grows.

CREATE INDEX idx_ci_runs_status_created_at ON ci_runs(status, created_at);
