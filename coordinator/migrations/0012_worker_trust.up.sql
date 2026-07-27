BEGIN;

-- Whether a worker's results are accepted directly or must clear quorum.
-- 'trusted'   — lab machine (shared token) or a verified/admin contributor.
-- 'untrusted' — a plain enthusiast; results are quarantined until quorum (C2).
CREATE TYPE worker_trust AS ENUM ('trusted', 'untrusted');

-- Who registered this worker (userservice user id, from the JWT sub). NULL for
-- workers registered with the shared service token. Not a foreign key: users
-- live in a separate service/database.
ALTER TABLE workers ADD COLUMN owner_id uuid;

-- Existing rows were all shared-token lab workers, hence 'trusted'.
ALTER TABLE workers ADD COLUMN trust_level worker_trust NOT NULL DEFAULT 'trusted';

CREATE INDEX ix_workers_owner ON workers (owner_id);

COMMIT;
