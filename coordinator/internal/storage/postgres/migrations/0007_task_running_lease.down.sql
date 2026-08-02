BEGIN;

ALTER TABLE tasks DROP CONSTRAINT IF EXISTS ck_tasks_leased_owner;
ALTER TABLE tasks ADD CONSTRAINT ck_tasks_leased_owner CHECK (
    status <> 'leased' OR (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
);

COMMIT;
