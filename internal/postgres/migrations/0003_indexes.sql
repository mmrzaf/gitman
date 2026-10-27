-- Retention scans for steps whose logs have not expired, and checks
-- whether a repository has any pending operation, on every pass.
CREATE INDEX idx_steps_logs_pending ON steps(logs_expired_at) WHERE logs_expired_at IS NULL;
CREATE INDEX idx_repository_operations_completed ON repository_operations(completed_at) WHERE completed_at IS NOT NULL;
