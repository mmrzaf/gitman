-- 013_index_access_tokens_user_id.up.sql
-- Add index on access_tokens.user_id for GetUserAccessTokens performance.

CREATE INDEX idx_access_tokens_user_id ON access_tokens(user_id);
