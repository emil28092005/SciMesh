BEGIN;

-- A running task holds a lease just like a leased one, so the lease-integrity
-- check must cover both states.
ALTER TABLE tasks DROP CONSTRAINT IF EXISTS ck_tasks_leased_owner;
ALTER TABLE tasks ADD CONSTRAINT ck_tasks_leased_owner CHECK (
    status NOT IN ('leased','running') OR (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
);

COMMIT;
