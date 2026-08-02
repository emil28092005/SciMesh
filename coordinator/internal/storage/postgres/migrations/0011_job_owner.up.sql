BEGIN;

-- Who submitted this job. Equals users.id from the userservice, taken from the
-- JWT `sub` claim. NOT a foreign key: users live in a separate service/database,
-- so integrity is guaranteed by the signed token, not by the DB.
--
-- Nullable because rows created before auth existed have no owner; new inserts
-- must supply it (enforced in the app, not the schema, during the MVP).
ALTER TABLE jobs ADD COLUMN owner_id uuid;

-- "List my jobs" / "admin filters by owner" scans by owner.
CREATE INDEX ix_jobs_owner ON jobs (owner_id);

COMMIT;
