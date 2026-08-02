BEGIN;

-- Quorum votes for a task computed by untrusted (volunteer) workers. A trusted
-- worker's result completes the task directly and never lands here; an untrusted
-- result is recorded as one vote, and the task is only completed once enough
-- distinct owners submit the same result_sha256.
--
-- One vote per (task, owner): a single volunteer cannot stuff the ballot by
-- running many workers under one account. A resubmission updates their vote.
CREATE TABLE task_results (
    task_id            uuid        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    owner_id           uuid        NOT NULL,
    result_sha256      text        NOT NULL,
    result_artifact_id uuid        NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    created_at         timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (task_id, owner_id)
);

-- Quorum check groups a task's votes by result_sha256.
CREATE INDEX ix_task_results_quorum ON task_results (task_id, result_sha256);

COMMIT;
