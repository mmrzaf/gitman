-- Events that name no repository cannot be scoped by repo_readers. Mark
-- the ones only an admin may see, such as people changes and a deleted
-- repository's name, so the instance feed never announces them to every
-- member.
ALTER TABLE events ADD COLUMN admin_only boolean NOT NULL DEFAULT false;

-- Existing person-level and repository-deletion events were already
-- visible to every member; mark them now so the fix covers history too.
UPDATE events SET admin_only = true WHERE action LIKE 'person.%' OR action = 'repo.deleted';
