-- 001_init.down.sql
-- Drop everything in reverse order with proper CASCADE handling.

-- Indexes
DROP INDEX IF EXISTS idx_repo_secrets_repo_id;
DROP INDEX IF EXISTS idx_repositories_webhook_secret;
DROP INDEX IF EXISTS idx_ci_runs_created_at;
DROP INDEX IF EXISTS idx_ci_runs_commit_hash;
DROP INDEX IF EXISTS idx_ci_runs_tag;
DROP INDEX IF EXISTS idx_ci_runs_branch;
DROP INDEX IF EXISTS idx_ci_runs_status;
DROP INDEX IF EXISTS idx_ci_runs_repo_id;
DROP INDEX IF EXISTS idx_ssh_keys_user_id;
DROP INDEX IF EXISTS idx_access_tokens_token_hash;
DROP INDEX IF EXISTS idx_repositories_owner_id;
DROP INDEX IF EXISTS idx_users_username;

-- Tables (order respects foreign keys)
DROP TABLE IF EXISTS repo_secrets;
DROP TABLE IF EXISTS ci_runs;
DROP TABLE IF EXISTS ssh_keys;
DROP TABLE IF EXISTS access_tokens;
DROP TABLE IF EXISTS repo_collaborators;
DROP TABLE IF EXISTS repositories;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
