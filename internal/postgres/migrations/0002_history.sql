-- History shows whether a push rewrote a branch's history; pushes recorded
-- before this column existed read as not forced.
ALTER TABLE push_updates ADD COLUMN is_force boolean NOT NULL DEFAULT false;

-- History lists the runs of a page of commits, by commit.
CREATE INDEX idx_runs_repo_commit ON runs(repo_id, commit_hash);
