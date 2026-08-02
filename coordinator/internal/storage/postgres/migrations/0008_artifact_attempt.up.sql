BEGIN;

-- A partial result belongs to the lease attempt that uploaded it. Without this
-- binding a worker holding a later retry could complete a task with stale bytes
-- uploaded by an expired attempt of that same task.
ALTER TABLE artifacts ADD COLUMN attempt integer;

-- A completed task never gets a later lease, so its current attempt is also
-- the attempt that produced the stored result.
UPDATE artifacts AS a
SET attempt = t.attempt
FROM tasks AS t
WHERE a.task_id = t.id
  AND a.kind = 'partial_result'::artifact_kind
  AND t.status = 'completed'::task_status
  AND a.attempt IS NULL;

-- For unfinished tasks the old schema cannot tell which attempt uploaded a
-- partial result. Keeping it would let a later retry claim stale bytes, so the
-- worker must upload again. Blob garbage is harmless and follows the existing
-- coordinator-owned storage cleanup policy.
DELETE FROM artifacts AS a
USING tasks AS t
WHERE a.task_id = t.id
  AND a.kind = 'partial_result'::artifact_kind
  AND t.status <> 'completed'::task_status
  AND a.attempt IS NULL;

ALTER TABLE artifacts ADD CONSTRAINT ck_partial_result_attempt
    CHECK (kind <> 'partial_result'::artifact_kind OR attempt IS NOT NULL);
ALTER TABLE artifacts ADD CONSTRAINT ck_artifact_attempt_positive
    CHECK (attempt IS NULL OR attempt > 0);

COMMIT;
