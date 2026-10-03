-- People: named individuals. Whether one can read a given repository
-- follows that repository's visibility and, for a restricted one, the
-- repo_readers table; ref_rules is the permission system for writing.
CREATE TABLE people (
    id            text PRIMARY KEY,
    username      text NOT NULL UNIQUE CHECK (username = lower(username)),
    password_hash text NOT NULL,
    auth_generation bigint NOT NULL DEFAULT 1,
    bootstrap_expires_at timestamptz,
    is_admin      boolean NOT NULL DEFAULT false,
    disabled_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    token_hash text PRIMARY KEY,
    person_id  text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    auth_generation bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX idx_sessions_person ON sessions(person_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

CREATE TABLE tokens (
    id           text PRIMARY KEY,
    person_id    text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    name         text NOT NULL,
    token_hash   text NOT NULL UNIQUE,
    all_repositories boolean NOT NULL DEFAULT true,
    auth_generation bigint NOT NULL,
    scope        text NOT NULL CHECK (scope IN ('read', 'write')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz
);

CREATE INDEX idx_tokens_person ON tokens(person_id);

-- Repositories are stored on disk by ID, never by name, so renaming a
-- repository never touches its Git storage, its runs or its logs.
CREATE TABLE repos (
    id             text PRIMARY KEY,
    name           text NOT NULL UNIQUE CHECK (name = lower(name)),
    description    text NOT NULL DEFAULT '',
    default_branch text NOT NULL DEFAULT 'main',
    -- run_counter hands out repository-local run numbers atomically:
    -- UPDATE ... SET run_counter = run_counter + 1 RETURNING run_counter.
    run_counter    bigint NOT NULL DEFAULT 0,
    -- visibility: "everyone" means every signed-in person can read it;
    -- "restricted" means only its repo_readers rows, and admins, can.
    visibility     text NOT NULL DEFAULT 'everyone' CHECK (visibility IN ('everyone', 'restricted')),
    -- default_push_policy/repo_push_people are who may push to a ref
    -- no ref_rules row matches, the same shape as a rule's own push
    -- policy below.
    default_push_policy text NOT NULL DEFAULT 'everyone' CHECK (default_push_policy IN ('everyone', 'admins', 'people')),
    created_by     text REFERENCES people(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- Access tokens must name their repositories explicitly.
CREATE TABLE token_repos (
 token_id text NOT NULL REFERENCES tokens(id) ON DELETE CASCADE,
 repo_id text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
 PRIMARY KEY (token_id, repo_id)
);

-- The explicit readers of a restricted repository. Irrelevant, but
-- harmless, for one visible to everyone.
CREATE TABLE repo_readers (
    repo_id    text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    person_id  text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    added_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (repo_id, person_id)
);

-- The current position of every branch and tag, kept current by the push
-- hooks rather than recomputed from Git on every page view.
CREATE TABLE refs (
    repo_id     text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN ('branch', 'tag')),
    name        text NOT NULL,
    commit_hash text NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  text REFERENCES people(id) ON DELETE SET NULL,
    PRIMARY KEY (repo_id, kind, name)
);

CREATE INDEX idx_refs_repo_updated ON refs(repo_id, updated_at DESC);

-- A ref rule matches branch or tag names by exact name or glob pattern
-- and is the only permission boundary in Gitman: who may push, and what a
-- pipeline run on that ref is allowed to do.
CREATE TABLE ref_rules (
    id            text PRIMARY KEY,
    repo_id       text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    kind          text NOT NULL CHECK (kind IN ('branch', 'tag')),
    pattern       text NOT NULL,
    push_policy   text NOT NULL CHECK (push_policy IN ('everyone', 'admins', 'people')) DEFAULT 'everyone',
    allow_force   boolean NOT NULL DEFAULT false,
    allow_delete  boolean NOT NULL DEFAULT false,
    run_on_push   boolean NOT NULL DEFAULT false,
    allow_docker  boolean NOT NULL DEFAULT false,
    allow_secrets boolean NOT NULL DEFAULT false,
    allow_deploy    boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    created_by    text REFERENCES people(id) ON DELETE SET NULL,
    UNIQUE (repo_id, kind, pattern)
);

CREATE TABLE repo_push_people (
 repo_id text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
 person_id text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
 PRIMARY KEY (repo_id, person_id)
);
CREATE TABLE rule_push_people (
 rule_id text NOT NULL REFERENCES ref_rules(id) ON DELETE CASCADE,
 person_id text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
 PRIMARY KEY (rule_id, person_id)
);

-- A push is one Git operation that moved one or more refs. push_updates
-- holds the individual ref movements it contained.
CREATE TABLE pushes (
    id         text PRIMARY KEY,
    repo_id    text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    person_id  text REFERENCES people(id) ON DELETE SET NULL,
    source_ip  text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_pushes_repo_created ON pushes(repo_id, created_at DESC);
CREATE INDEX idx_pushes_created ON pushes(created_at DESC);

CREATE TABLE push_updates (
    id           text PRIMARY KEY,
    push_id      text NOT NULL REFERENCES pushes(id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('branch', 'tag')),
    name         text NOT NULL,
    old_commit   text NOT NULL,
    new_commit   text NOT NULL,
    is_create    boolean NOT NULL DEFAULT false,
    is_delete    boolean NOT NULL DEFAULT false,
    is_force     boolean NOT NULL DEFAULT false,
    -- commit_count is exact up to a cap; commit_count_capped marks a
    -- push whose count reached that cap, so the UI can show "1000+"
    -- instead of a number it never finished counting.
    commit_count        integer NOT NULL DEFAULT 0,
    commit_count_capped boolean NOT NULL DEFAULT false
);

CREATE INDEX idx_push_updates_push ON push_updates(push_id);

CREATE TABLE workers (
    engine_id text NOT NULL DEFAULT '',
    workspace_root text NOT NULL DEFAULT '',
    id           text PRIMARY KEY,
    hostname     text NOT NULL,
    started_at   timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    active_runs  integer NOT NULL DEFAULT 0,
    ready boolean NOT NULL DEFAULT false,
    readiness_reason text NOT NULL DEFAULT 'Starting',
    images text[] NOT NULL DEFAULT '{}',
    readiness_at timestamptz,
    stopped_at   timestamptz
);


-- A run is one pipeline execution for one commit. number is a
-- repository-local sequence, so runs read as "#142" rather than by their
-- opaque ID. target and version are resolved from the pipeline file at
-- creation time, not guessed later from log text.
CREATE TABLE runs (
    id           text PRIMARY KEY,
    repo_id      text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    number       bigint NOT NULL,
    commit_hash  text NOT NULL,
    ref_kind     text NOT NULL CHECK (ref_kind IN ('branch', 'tag')),
    ref_name     text NOT NULL CHECK (ref_name <> ''),
    trigger      text NOT NULL CHECK (trigger IN ('push', 'manual')),
    triggered_by text REFERENCES people(id) ON DELETE SET NULL,
    push_id      text REFERENCES pushes(id) ON DELETE SET NULL,
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'passed', 'failed', 'cancelled')),
    reason       text NOT NULL DEFAULT '',
    target       text NOT NULL DEFAULT '',
    version      text NOT NULL DEFAULT '',
    -- allow_secrets snapshots the ref rule's decision when the run was
    -- created, so editing a rule never changes what a queued run gets.
    allow_secrets boolean NOT NULL DEFAULT false,
    -- pipeline is the pipeline file a queued run was created from: the
    -- exact bytes its ref's rules were checked against, which its worker
    -- runs instead of re-reading the file from its own checkout.
    pipeline     bytea,
    required_images text[] NOT NULL DEFAULT '{}',
    timeout_ns bigint NOT NULL DEFAULT 0 CHECK (timeout_ns >= 0),
    deadline_at timestamptz,
    -- cancel_requested asks the worker running this run to stop it.
    cancel_requested boolean NOT NULL DEFAULT false,
    worker_id    text REFERENCES workers(id) ON DELETE SET NULL,
    -- fetch_token_hash is the hash of the token the worker running this
    -- run uses to fetch its commit over Gitman's Git HTTP. It is set when
    -- a worker claims the run and cleared when the run finishes.
    fetch_token_hash text,
    queued_at    timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz,
    UNIQUE (repo_id, number),
    -- Lets a deployment name its run and its repository together, so the
    -- two can never disagree.
    UNIQUE (id, repo_id),
    -- A run has finished exactly when it is neither queued nor running; a
    -- queued run has not started; a running one has, on some worker.
    CONSTRAINT runs_finished_matches_status CHECK ((status IN ('queued', 'running')) = (finished_at IS NULL)),
    CONSTRAINT runs_queued_not_started CHECK (status <> 'queued' OR started_at IS NULL),
    CONSTRAINT runs_running_on_a_worker CHECK (status <> 'running' OR (started_at IS NOT NULL AND worker_id IS NOT NULL))
);

CREATE INDEX idx_runs_repo_queued ON runs(repo_id, queued_at DESC);
CREATE INDEX idx_runs_status ON runs(status) WHERE status IN ('queued', 'running');
-- Commit-specific per-ref summaries and current-head retention.
CREATE INDEX idx_runs_repo_ref ON runs(repo_id, ref_kind, ref_name, number DESC);
CREATE INDEX idx_runs_repo_ref_commit ON runs(repo_id, ref_kind, ref_name, commit_hash, number DESC);
-- Deleting a push, a repository's pushes or a worker clears these
-- references; without an index each such delete scans every run.
CREATE INDEX idx_runs_push ON runs(push_id);
CREATE INDEX idx_runs_worker ON runs(worker_id);
CREATE UNIQUE INDEX idx_runs_fetch_token ON runs(fetch_token_hash) WHERE fetch_token_hash IS NOT NULL;
-- The instance-wide activity feed lists finished runs newest first
-- across every repository; the Repository page needs the same, scoped
-- to one; retention finds old ones regardless of repository.
CREATE INDEX idx_runs_finished ON runs(finished_at DESC) WHERE finished_at IS NOT NULL;
CREATE INDEX idx_runs_repo_finished ON runs(repo_id, finished_at DESC) WHERE finished_at IS NOT NULL;
-- Claiming a run selects the oldest queued one in this exact order.
CREATE INDEX idx_runs_queue_order ON runs(queued_at, id) WHERE status = 'queued';

-- A step is a separately executed, separately recorded unit of a run.
-- Status is written by the worker as each step actually starts and ends;
-- nothing in Gitman infers step state from log text.
CREATE TABLE steps (
    id          text PRIMARY KEY,
    run_id      text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    index       integer NOT NULL,
    name        text NOT NULL,
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'passed', 'failed', 'skipped', 'cancelled')),
    exit_code   integer,
    type text NOT NULL DEFAULT 'run' CHECK (type IN ('run', 'deploy')),
    deployment_generation bigint,
    container_name text,
    container_id text,
    container_removed_at timestamptz,
    logs_expired_at timestamptz,
    log_recording_error text NOT NULL DEFAULT '',
    log_bytes bigint NOT NULL DEFAULT 0,
    log_lines bigint NOT NULL DEFAULT 0,
    started_at  timestamptz,
    finished_at timestamptz,
    UNIQUE (run_id, index)
);

-- A target owner is released only after confirmed termination. Heartbeat
-- expiry never grants another deployment permission to start.
CREATE TABLE deployment_targets (
 repo_id text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
 target text NOT NULL,
 generation bigint NOT NULL DEFAULT 0,
 latest_run_number bigint NOT NULL DEFAULT 0,
 owner_run_id text REFERENCES runs(id) ON DELETE RESTRICT,
 owner_step_id text REFERENCES steps(id) ON DELETE RESTRICT,
 PRIMARY KEY (repo_id, target),
 CHECK ((owner_run_id IS NULL) = (owner_step_id IS NULL))
);
CREATE INDEX idx_deployment_targets_owner ON deployment_targets(owner_run_id) WHERE owner_run_id IS NOT NULL;

CREATE TABLE step_logs (
    repo_id  text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    step_id  text NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    sequence integer NOT NULL,
    content  text NOT NULL,
    byte_len integer NOT NULL CHECK (byte_len BETWEEN 0 AND 65536),
    line_count integer NOT NULL,
    PRIMARY KEY (step_id, sequence)
);

-- Lines a step writes to $GITMAN_SUMMARY, shown on the run page instead of
-- being reconstructed by reading the log.
CREATE TABLE run_summary (
    run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    key    text NOT NULL,
    value  text NOT NULL,
    PRIMARY KEY (run_id, key)
);

-- A deployment records a successful explicit deploy script. It does not
-- attest that an application is healthy.
CREATE TABLE deployments (
    step_id text UNIQUE,
    id          text PRIMARY KEY,
    repo_id     text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    target      text NOT NULL,
    version     text NOT NULL,
    commit_hash text NOT NULL,
    -- Deployments outlive the runs that made them: retention prunes old
    -- runs, while the latest target record survives deployment retention.
    run_id      text,
    person_id   text REFERENCES people(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (run_id, repo_id) REFERENCES runs(id, repo_id) ON DELETE SET NULL (run_id)
);

-- Pruning a run clears its deployments' reference to it.
CREATE INDEX idx_deployments_run ON deployments(run_id);

CREATE INDEX idx_deployments_repo_target ON deployments(repo_id, target, created_at DESC);
-- The instance-wide and per-repository activity feeds both list
-- deployments newest first across every target, which idx_deployments_
-- repo_target above cannot serve since it orders by target first.
CREATE INDEX idx_deployments_repo_created ON deployments(repo_id, created_at DESC);
CREATE INDEX idx_deployments_created ON deployments(created_at DESC);

CREATE TABLE secrets (
    id         text PRIMARY KEY,
    repo_id    text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    key        text NOT NULL,
    ciphertext bytea NOT NULL,
    nonce      bytea NOT NULL CHECK (octet_length(nonce) = 12),
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by text REFERENCES people(id) ON DELETE SET NULL,
    UNIQUE (repo_id, key)
);

-- The instance-wide activity feed: settings changes and anything else
-- worth showing on Home besides pushes and runs, which have their own
-- tables.
CREATE TABLE events (
    id         text PRIMARY KEY,
    repo_id    text REFERENCES repos(id) ON DELETE CASCADE,
    person_id  text REFERENCES people(id) ON DELETE SET NULL,
    action     text NOT NULL,
    detail     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_events_repo_created ON events(repo_id, created_at DESC);
CREATE INDEX idx_events_created ON events(created_at DESC);

-- The one row naming this Gitman instance. Workers label their step
-- containers with it, so instances that share a Docker host each clean up
-- only their own.
CREATE TABLE instance (
    maintenance boolean NOT NULL DEFAULT false,
    id        text NOT NULL,
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton)
);

INSERT INTO instance (id) VALUES (gen_random_uuid()::text);

-- The runs of a page of commits, by commit.
CREATE INDEX idx_runs_repo_commit ON runs(repo_id, commit_hash);

-- Refused pushes and their reasons. No public ref moved.
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

-- Grant sets are normalized and must be complete when the transaction commits.
CREATE FUNCTION check_repo_push_grants() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner_id text;
BEGIN
 IF TG_TABLE_NAME = 'repos' THEN owner_id := COALESCE(NEW.id, OLD.id);
 ELSE owner_id := COALESCE(NEW.repo_id, OLD.repo_id); END IF;
 IF EXISTS (SELECT 1 FROM repos WHERE id = owner_id AND default_push_policy = 'people')
    AND NOT EXISTS (SELECT 1 FROM repo_push_people WHERE repo_id = owner_id) THEN
  RAISE EXCEPTION 'named push policy requires people' USING ERRCODE = '23514';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER repo_push_policy_complete AFTER INSERT OR UPDATE ON repos
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_repo_push_grants();
CREATE CONSTRAINT TRIGGER repo_push_grants_complete AFTER INSERT OR UPDATE OR DELETE ON repo_push_people
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_repo_push_grants();

CREATE FUNCTION check_rule_push_grants() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner_id text;
BEGIN
 IF TG_TABLE_NAME = 'ref_rules' THEN owner_id := COALESCE(NEW.id, OLD.id);
 ELSE owner_id := COALESCE(NEW.rule_id, OLD.rule_id); END IF;
 IF EXISTS (SELECT 1 FROM ref_rules WHERE id = owner_id AND push_policy = 'people')
    AND NOT EXISTS (SELECT 1 FROM rule_push_people WHERE rule_id = owner_id) THEN
  RAISE EXCEPTION 'named push policy requires people' USING ERRCODE = '23514';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER rule_push_policy_complete AFTER INSERT OR UPDATE ON ref_rules
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_rule_push_grants();
CREATE CONSTRAINT TRIGGER rule_push_grants_complete AFTER INSERT OR UPDATE OR DELETE ON rule_push_people
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_rule_push_grants();

CREATE FUNCTION check_token_repositories() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner_id text;
BEGIN
 IF TG_TABLE_NAME = 'tokens' THEN owner_id := COALESCE(NEW.id, OLD.id);
 ELSE owner_id := COALESCE(NEW.token_id, OLD.token_id); END IF;
 IF EXISTS (SELECT 1 FROM tokens WHERE id = owner_id AND NOT all_repositories)
    AND NOT EXISTS (SELECT 1 FROM token_repos WHERE token_id = owner_id) THEN
  IF TG_TABLE_NAME = 'token_repos' AND TG_OP = 'DELETE' THEN
   DELETE FROM tokens WHERE id = owner_id;
  ELSE
   RAISE EXCEPTION 'access token requires repositories' USING ERRCODE = '23514';
  END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER token_repositories_complete AFTER INSERT OR UPDATE ON tokens
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_token_repositories();
CREATE CONSTRAINT TRIGGER token_grants_complete AFTER INSERT OR UPDATE OR DELETE ON token_repos
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_token_repositories();

-- Intent is durable before filesystem mutations. Completed receipts are kept
-- independently from repository rows so deletion can be reconciled after a crash.
CREATE TABLE repository_operations (
    id text PRIMARY KEY,
    repo_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('create','delete','head','push')),
    name text NOT NULL DEFAULT '',
    payload jsonb NOT NULL,
    actor_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    error text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX repository_operations_pending_repo ON repository_operations(repo_id) WHERE completed_at IS NULL;
CREATE UNIQUE INDEX repository_operations_pending_name ON repository_operations(name) WHERE kind = 'create' AND completed_at IS NULL;

CREATE TABLE repository_storage (
 repo_id text PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
 log_bytes bigint NOT NULL DEFAULT 0 CHECK(log_bytes BETWEEN 0 AND 1073741824)
);

CREATE FUNCTION identify_log_repository() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 SELECT r.repo_id INTO NEW.repo_id FROM steps s JOIN runs r ON r.id=s.run_id WHERE s.id=NEW.step_id;
 RETURN NEW;
END $$;
CREATE TRIGGER log_repository BEFORE INSERT ON step_logs FOR EACH ROW EXECUTE FUNCTION identify_log_repository();

CREATE FUNCTION account_repository_logs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  UPDATE repository_storage SET log_bytes=log_bytes-octet_length(OLD.content) WHERE repo_id=OLD.repo_id;
  RETURN OLD;
 END IF;
 INSERT INTO repository_storage(repo_id,log_bytes) VALUES(NEW.repo_id,octet_length(NEW.content))
 ON CONFLICT(repo_id) DO UPDATE SET log_bytes=repository_storage.log_bytes+EXCLUDED.log_bytes
 WHERE repository_storage.log_bytes+EXCLUDED.log_bytes<=1073741824;
 IF NOT FOUND THEN
  RAISE EXCEPTION 'repository log budget exceeded' USING ERRCODE='23514',CONSTRAINT='repository_log_budget';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER log_storage AFTER INSERT OR DELETE ON step_logs FOR EACH ROW EXECUTE FUNCTION account_repository_logs();

-- Immutable commit metadata makes the home read model entirely SQL-backed.
CREATE TABLE commit_metadata (
 repo_id text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
 hash text NOT NULL,
 subject text NOT NULL,
 author_name text NOT NULL,
 author_email text NOT NULL,
 authored_at timestamptz,
 PRIMARY KEY(repo_id,hash)
);
