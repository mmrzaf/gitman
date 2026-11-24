-- Backfill deployment source refs from the run that made them. Rows
-- written before 1.0.0 carry the column default ''; a row whose run has
-- already been pruned stays unknown on purpose, because a ref inferred from
-- today's heads could be wrong and a wrong drift warning is worse than none.
UPDATE deployments d
   SET source_kind = r.ref_kind, source_name = r.ref_name
  FROM runs r
 WHERE d.run_id = r.id
   AND d.source_kind = ''
   AND r.ref_kind IN ('branch', 'tag')
   AND r.ref_name <> '';

-- One index dive per ref instead of sorting every deployment of the repo.
CREATE INDEX idx_deployments_repo_source
    ON deployments(repo_id, source_kind, source_name, created_at DESC, id DESC);

-- Keep the last successful trim separate from the last attempt, so a failed
-- enforcement no longer overwrites the success time.
ALTER TABLE image_hosts ADD COLUMN cache_attempt_at timestamptz;
