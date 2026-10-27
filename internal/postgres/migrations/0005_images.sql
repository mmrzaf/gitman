-- Images on a worker host, with the last known build-cache policy and prune
-- result. One row per Docker engine, not per worker process: workers sharing a
-- host share an inventory and must not act on the same images twice.
CREATE TABLE image_hosts (
    host_id text PRIMARY KEY,
    worker_id text REFERENCES workers(id) ON DELETE SET NULL,
    hostname text NOT NULL DEFAULT '',
    collected_at timestamptz NOT NULL DEFAULT now(),
    images jsonb NOT NULL DEFAULT '[]'::jsonb,
    cache_size_bytes bigint NOT NULL DEFAULT 0,
    cache_reclaimable_bytes bigint NOT NULL DEFAULT 0,
    cache_keep_gib integer NOT NULL DEFAULT 0,
    cache_older_than_hours integer NOT NULL DEFAULT 0,
    cache_disabled boolean NOT NULL DEFAULT false,
    automation_mode text NOT NULL DEFAULT 'off',
    cache_pruned_at timestamptz,
    cache_reclaimed_bytes bigint NOT NULL DEFAULT 0,
    cache_error text NOT NULL DEFAULT ''
);

-- Image operations are durable work targeted at a Docker host. They live in
-- the same operations journal as repository intents so there is one queue, one
-- Operations page and one retention rule; a host-targeted row has no repository.
ALTER TABLE repository_operations ALTER COLUMN repo_id DROP NOT NULL;
ALTER TABLE repository_operations ADD COLUMN engine_id text NOT NULL DEFAULT '';
ALTER TABLE repository_operations ADD COLUMN worker_id text REFERENCES workers(id) ON DELETE SET NULL;
ALTER TABLE repository_operations ADD COLUMN claimed_at timestamptz;
ALTER TABLE repository_operations ADD COLUMN attempts integer NOT NULL DEFAULT 0;
ALTER TABLE repository_operations ADD COLUMN result jsonb;

ALTER TABLE repository_operations DROP CONSTRAINT IF EXISTS repository_operations_kind_check;
ALTER TABLE repository_operations ADD CONSTRAINT repository_operations_kind_check
    CHECK (kind IN ('create', 'delete', 'head', 'push', 'image.remove', 'image.prune', 'cache.prune'));
ALTER TABLE repository_operations ADD CONSTRAINT repository_operations_target_check
    CHECK ((kind IN ('image.remove', 'image.prune', 'cache.prune')) = (repo_id IS NULL));

DROP INDEX repository_operations_pending_repo;
CREATE UNIQUE INDEX repository_operations_pending_repo ON repository_operations(repo_id)
    WHERE completed_at IS NULL AND repo_id IS NOT NULL;

CREATE INDEX idx_repository_operations_image_pending ON repository_operations(engine_id, created_at)
    WHERE completed_at IS NULL AND kind IN ('image.remove', 'image.prune', 'cache.prune');
CREATE INDEX idx_repository_operations_image_recent ON repository_operations(engine_id, created_at DESC)
    WHERE kind IN ('image.remove', 'image.prune', 'cache.prune');
