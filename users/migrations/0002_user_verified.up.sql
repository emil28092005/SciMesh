BEGIN;

-- A "verified" account is a trusted contributor: the coordinator accepts its
-- workers' results directly, without quorum cross-checking. Distinct from role
-- (which governs what a user may do with their own jobs). Granted by an admin,
-- never self-served; defaults to false, so a fresh account is untrusted.
ALTER TABLE users ADD COLUMN verified boolean NOT NULL DEFAULT false;

COMMIT;
