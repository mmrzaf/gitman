-- People: named individuals. Whether one can read a given repository
-- follows that repository's visibility and, for a restricted one, the
-- repo_readers table; ref_rules is the permission system for writing.
CREATE TABLE people (
    id            text PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    is_admin      boolean NOT NULL DEFAULT false,
    disabled_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Usernames differ by more than case, so "Darius" and "darius" are never
-- two people.
CREATE UNIQUE INDEX idx_people_username_lower ON people (lower(username));

CREATE TABLE sessions (
    token_hash text PRIMARY KEY,
    person_id  text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
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
    scope        text NOT NULL CHECK (scope IN ('read', 'write')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz,
    last_used_at timestamptz
);

CREATE INDEX idx_tokens_person ON tokens(person_id);

-- Repositories are stored on disk by ID, never by name, so renaming a
-- repository never touches its Git storage, its runs or its logs.
CREATE TABLE repos (
    id             text PRIMARY KEY,
    name           text NOT NULL UNIQUE,
    description    text NOT NULL DEFAULT '',
    default_branch text NOT NULL DEFAULT 'main',
    -- run_counter hands out repository-local run numbers atomically:
    -- UPDATE ... SET run_counter = run_counter + 1 RETURNING run_counter.
    run_counter    bigint NOT NULL DEFAULT 0,
    -- visibility: "everyone" means every signed-in person can read it;
    -- "restricted" means only its repo_readers rows, and admins, can.
    visibility     text NOT NULL DEFAULT 'everyone' CHECK (visibility IN ('everyone', 'restricted')),
    -- default_push_policy/default_push_people are who may push to a ref
    -- no ref_rules row matches, the same shape as a rule's own push
    -- policy below.
    default_push_policy text NOT NULL DEFAULT 'everyone' CHECK (default_push_policy IN ('everyone', 'admins', 'people')),
    default_push_people text[] NOT NULL DEFAULT '{}',
    CHECK (default_push_policy != 'people' OR cardinality(default_push_people) > 0),
    created_by     text REFERENCES people(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- Repository names differ by more than case, so /App and /app never name
-- two repositories.
CREATE UNIQUE INDEX idx_repos_name_lower ON repos (lower(name));

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
    push_people   text[] NOT NULL DEFAULT '{}',
    -- cardinality(), not array_length(push_people, 1): the latter returns
    -- NULL rather than 0 for an empty array, which a CHECK treats as
    -- satisfied and so would let this rule name nobody.
    CHECK (push_policy != 'people' OR cardinality(push_people) > 0),
    allow_force   boolean NOT NULL DEFAULT false,
    allow_delete  boolean NOT NULL DEFAULT false,
    run_on_push   boolean NOT NULL DEFAULT false,
    allow_docker  boolean NOT NULL DEFAULT false,
    allow_secrets boolean NOT NULL DEFAULT false,
    allow_ship    boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    created_by    text REFERENCES people(id) ON DELETE SET NULL,
    UNIQUE (repo_id, kind, pattern)
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
    -- commit_count is exact up to a cap; commit_count_capped marks a
    -- push whose count reached that cap, so the UI can show "1000+"
    -- instead of a number it never finished counting.
    commit_count        integer NOT NULL DEFAULT 0,
    commit_count_capped boolean NOT NULL DEFAULT false
);

CREATE INDEX idx_push_updates_push ON push_updates(push_id);

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
    -- cancel_requested asks the worker running this run to stop it.
    cancel_requested boolean NOT NULL DEFAULT false,
    worker_id    text,
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
-- The latest run of each ref: the Repository page's per-ref status, and
-- the run retention always keeps.
CREATE INDEX idx_runs_repo_ref ON runs(repo_id, ref_kind, ref_name, number DESC);
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
    started_at  timestamptz,
    finished_at timestamptz,
    UNIQUE (run_id, index)
);

CREATE TABLE step_logs (
    step_id  text NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    sequence integer NOT NULL,
    content  text NOT NULL,
    byte_len integer NOT NULL,
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

-- A deployment records that a run shipped a version to a target. This is
-- what makes "what's live" a query instead of something reconstructed from
-- memory.
CREATE TABLE deployments (
    id          text PRIMARY KEY,
    repo_id     text NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    target      text NOT NULL,
    version     text NOT NULL,
    commit_hash text NOT NULL,
    -- Deployments outlive the runs that made them: retention prunes old
    -- runs, but what shipped where, and when, is kept forever.
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
    nonce      bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by text REFERENCES people(id) ON DELETE SET NULL,
    UNIQUE (repo_id, key)
);

CREATE TABLE workers (
    id           text PRIMARY KEY,
    hostname     text NOT NULL,
    started_at   timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    active_runs  integer NOT NULL DEFAULT 0,
    stopped_at   timestamptz
);

-- runs.worker_id names the worker that claimed it; the constraint is
-- added here because workers is created after runs. A pruned worker's
-- past runs keep their history with no worker attached, rather than
-- blocking the prune.
ALTER TABLE runs ADD CONSTRAINT runs_worker_id_fkey
    FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE SET NULL;

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
    id        text NOT NULL,
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton)
);

INSERT INTO instance (id) VALUES (gen_random_uuid()::text);
