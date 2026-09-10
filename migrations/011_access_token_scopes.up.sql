-- 011_access_token_scopes.up.sql
-- Legacy tokens keep their beta-17/beta-18 capabilities while new tokens can
-- be restricted to clone/fetch and read-only API access.

ALTER TABLE access_tokens
ADD COLUMN scope TEXT NOT NULL DEFAULT 'repo:write'
CHECK (scope IN ('repo:read', 'repo:write'));
