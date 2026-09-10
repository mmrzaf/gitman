-- 009_ci_worker_health.up.sql
-- Persist CI worker liveness and admission health so operators can distinguish
-- a healthy web process from an unavailable runner fleet.

CREATE TABLE ci_workers (
    id             TEXT PRIMARY KEY,
    hostname       TEXT NOT NULL,
    pid            INTEGER NOT NULL,
    concurrency    INTEGER NOT NULL CHECK (concurrency > 0),
    healthy        INTEGER NOT NULL DEFAULT 0 CHECK (healthy IN (0, 1)),
    status_message TEXT NOT NULL DEFAULT '',
    active_jobs    INTEGER NOT NULL DEFAULT 0 CHECK (active_jobs >= 0),
    started_at     INTEGER NOT NULL,
    heartbeat_at   INTEGER NOT NULL,
    stopped_at     INTEGER
);

CREATE INDEX idx_ci_workers_heartbeat_at ON ci_workers(heartbeat_at DESC);
