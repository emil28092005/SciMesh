BEGIN;

CREATE TYPE job_status  AS ENUM ('pending','running','completed','failed','cancelled');
CREATE TYPE task_status AS ENUM ('pending','leased','completed','failed','cancelled');

-- One user submission, possibly split into several tasks.
CREATE TABLE jobs (
    id           uuid        PRIMARY KEY,
    workload     text        NOT NULL,
    input_uri    text        NOT NULL,
    parameters   jsonb       NOT NULL DEFAULT '{}'::jsonb,
    status       job_status  NOT NULL DEFAULT 'pending',
    created_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

-- One independently executable chunk.
CREATE TABLE tasks (
    id               uuid        PRIMARY KEY,
    job_id           uuid        NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    chunk_index      integer     NOT NULL,
    workload         text        NOT NULL,
    input_uri        text        NOT NULL,
    input_sha256     text        NOT NULL,
    parameters       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    status           task_status NOT NULL DEFAULT 'pending',
    attempt          integer     NOT NULL DEFAULT 0,
    max_attempts     integer     NOT NULL DEFAULT 3,
    lease_owner      text,
    lease_expires_at timestamptz,
    result_uri       text,
    result_sha256    text,
    metrics          jsonb,
    error_code       text,
    error_message    text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    started_at       timestamptz,
    completed_at     timestamptz,
    version          integer     NOT NULL DEFAULT 0,

    CONSTRAINT uq_tasks_job_chunk    UNIQUE (job_id, chunk_index),
    CONSTRAINT ck_tasks_attempt      CHECK (attempt >= 0),
    CONSTRAINT ck_tasks_max_attempts CHECK (max_attempts > 0),
    -- A completed task must carry its result manifest.
    CONSTRAINT ck_tasks_completed_result CHECK (
        status <> 'completed' OR (result_uri IS NOT NULL AND result_sha256 IS NOT NULL)
    ),
    -- A leased task must carry its lease.
    CONSTRAINT ck_tasks_leased_owner CHECK (
        status <> 'leased' OR (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
    )
);

-- Claim path: find the oldest pending task fast.
CREATE INDEX ix_tasks_claim ON tasks (status, lease_expires_at, created_at);
CREATE INDEX ix_tasks_job   ON tasks (job_id);

COMMIT;
