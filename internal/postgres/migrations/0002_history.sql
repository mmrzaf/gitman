-- What History shows beyond what beta 21 recorded.

-- Whether a push rewrote a branch's history; pushes recorded before this
-- column existed read as not forced.
ALTER TABLE push_updates ADD COLUMN is_force boolean NOT NULL DEFAULT false;

-- The runs of a page of commits, by commit.
CREATE INDEX idx_runs_repo_commit ON runs(repo_id, commit_hash);

-- A push Gitman refused, and why. Git tells the pusher only "pre-receive
-- hook declined" for each ref, so History → Activity keeps the reasons the
-- hook printed. Nothing here was accepted: no ref moved.
CREATE TABLE push_refusals (
    id         text PRIMARY KEY,
    repo_id    text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    person_id  text REFERENCES people(id) ON DELETE SET NULL,
    source_ip  text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_push_refusals_repo_created ON push_refusals(repo_id, created_at DESC);

-- One row per refused ref, in the order the hook reported them. ref is the
-- full ref name, empty when the whole push was refused, such as one from a
-- person who is disabled.
CREATE TABLE push_refusal_refs (
    refusal_id text NOT NULL REFERENCES push_refusals(id) ON DELETE CASCADE,
    position   integer NOT NULL,
    ref        text NOT NULL,
    reason     text NOT NULL,
    PRIMARY KEY (refusal_id, position)
);
