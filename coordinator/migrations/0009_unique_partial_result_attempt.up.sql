BEGIN;

-- Old deployments can contain more than one partial result because earlier
-- versions accepted repeated PUTs. Preserve the one referenced by a completed
-- task and discard stale rows; unfinished tasks must upload again after a
-- deploy, just as they do after a lost lease.
DELETE FROM artifacts AS a
USING tasks AS t
WHERE a.task_id = t.id
  AND a.kind = 'partial_result'::artifact_kind
  AND t.status <> 'completed'::task_status;

DELETE FROM artifacts AS a
USING tasks AS t
WHERE a.task_id = t.id
  AND a.kind = 'partial_result'::artifact_kind
  AND t.status = 'completed'::task_status
  AND a.id <> t.result_artifact_id;

-- One lease attempt has one durable partial result. This makes an upload retry
-- idempotent and prevents repeated uploads from accumulating orphan artifacts.
CREATE UNIQUE INDEX uq_partial_result_task_attempt
    ON artifacts (task_id, attempt)
    WHERE kind = 'partial_result'::artifact_kind;

COMMIT;
