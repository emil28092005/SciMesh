BEGIN;

-- Results are now coordinator-owned artifacts, not worker-supplied URIs.
-- Drop the URI-based completion guard and columns, and require a completed task
-- to reference its result artifact instead (PLAN.md §6.2).
ALTER TABLE tasks DROP CONSTRAINT IF EXISTS ck_tasks_completed_result;
ALTER TABLE tasks DROP COLUMN IF EXISTS result_uri;
ALTER TABLE tasks DROP COLUMN IF EXISTS result_sha256;

ALTER TABLE tasks ADD CONSTRAINT ck_tasks_completed_result CHECK (
    status <> 'completed' OR result_artifact_id IS NOT NULL
);

COMMIT;
