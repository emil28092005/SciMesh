BEGIN;

ALTER TABLE tasks DROP CONSTRAINT IF EXISTS ck_tasks_has_input;

-- Restoring NOT NULL requires the columns to be populated; safe on a fresh DB.
ALTER TABLE tasks ALTER COLUMN input_uri SET NOT NULL;
ALTER TABLE jobs  ALTER COLUMN input_uri SET NOT NULL;

COMMIT;
