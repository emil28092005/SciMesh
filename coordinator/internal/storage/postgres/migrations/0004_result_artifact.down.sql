BEGIN;

ALTER TABLE tasks DROP CONSTRAINT IF EXISTS ck_tasks_completed_result;
ALTER TABLE tasks ADD COLUMN result_uri    text;
ALTER TABLE tasks ADD COLUMN result_sha256 text;

ALTER TABLE tasks ADD CONSTRAINT ck_tasks_completed_result CHECK (
    status <> 'completed' OR (result_uri IS NOT NULL AND result_sha256 IS NOT NULL)
);

COMMIT;
