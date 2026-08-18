-- Remove the obsolete HTTP webhook credential. Push-triggered CI is delivered
-- locally through Gitman's managed durable post-receive queue.
DROP INDEX IF EXISTS idx_repositories_webhook_secret;
ALTER TABLE repositories DROP COLUMN webhook_secret;
