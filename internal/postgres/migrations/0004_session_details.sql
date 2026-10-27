-- Sessions get an opaque ID for listing and revocation, so the cookie's
-- token hash is never exposed to a page or a command, and the details a
-- person needs to recognise a session.
ALTER TABLE sessions ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE sessions ADD CONSTRAINT sessions_id_key UNIQUE (id);
ALTER TABLE sessions ADD COLUMN last_seen_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE sessions ADD COLUMN ip text NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN user_agent text NOT NULL DEFAULT '';
