-- 008_security_hardening.up.sql
-- Add bounded-lifetime access-token metadata and a durable security audit trail.

ALTER TABLE access_tokens ADD COLUMN expires_at INTEGER;
ALTER TABLE access_tokens ADD COLUMN last_used_at INTEGER;
-- Existing beta-17 credentials receive a 90-day rotation window instead of
-- remaining permanent credentials indefinitely.
UPDATE access_tokens
SET expires_at = strftime('%s', 'now') + (90 * 24 * 60 * 60)
WHERE expires_at IS NULL;
CREATE INDEX idx_access_tokens_expires_at ON access_tokens(expires_at);

CREATE TABLE audit_events (
    id             TEXT PRIMARY KEY,
    actor_user_id  TEXT,
    actor_username TEXT NOT NULL DEFAULT '',
    action         TEXT NOT NULL,
    target_type    TEXT NOT NULL DEFAULT '',
    target_id      TEXT NOT NULL DEFAULT '',
    source_ip      TEXT NOT NULL DEFAULT '',
    request_id     TEXT NOT NULL DEFAULT '',
    metadata_json  TEXT NOT NULL DEFAULT '{}',
    created_at     INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    FOREIGN KEY(actor_user_id) REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_audit_events_created_at ON audit_events(created_at DESC);
CREATE INDEX idx_audit_events_actor_user_id ON audit_events(actor_user_id);
CREATE INDEX idx_audit_events_action ON audit_events(action);
