-- 010_audit_target_index.up.sql
-- Account security history queries include events that target a user even when
-- the event has no authenticated actor (for example failed logins).

CREATE INDEX idx_audit_events_target ON audit_events(target_type, target_id, created_at DESC);
