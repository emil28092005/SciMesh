BEGIN;

CREATE TYPE artifact_kind AS ENUM ('input','shard','partial_result','final_result','log');

-- A durable file the coordinator owns: input, shard, partial/final result, log.
-- The database is the source of truth; files are found through this metadata,
-- never by scanning directories.
CREATE TABLE artifacts (
    id           uuid          PRIMARY KEY,
    job_id       uuid          NOT NULL REFERENCES jobs(id)  ON DELETE CASCADE,
    task_id      uuid          REFERENCES tasks(id) ON DELETE CASCADE, -- null for job-level inputs
    kind         artifact_kind NOT NULL,
    filename     text          NOT NULL,
    storage_key  text          NOT NULL UNIQUE,  -- coordinator-generated, never a client path
    content_type text          NOT NULL DEFAULT 'application/octet-stream',
    size_bytes   bigint        NOT NULL CHECK (size_bytes >= 0),
    sha256       text          NOT NULL,
    created_at   timestamptz   NOT NULL DEFAULT now()
);

CREATE INDEX ix_artifacts_job  ON artifacts (job_id);
CREATE INDEX ix_artifacts_task ON artifacts (task_id);

-- Jobs and tasks reference their artifacts. Nullable during the transition from
-- URI-based inputs/results to artifact-based ones.
ALTER TABLE jobs  ADD COLUMN input_artifact_id  uuid REFERENCES artifacts(id);
ALTER TABLE jobs  ADD COLUMN result_artifact_id uuid REFERENCES artifacts(id);
ALTER TABLE tasks ADD COLUMN input_artifact_id  uuid REFERENCES artifacts(id);
ALTER TABLE tasks ADD COLUMN result_artifact_id uuid REFERENCES artifacts(id);

COMMIT;
