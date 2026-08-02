-- 0001: core schema. SQLite stores enums as TEXT with CHECK constraints and
-- JSON documents as TEXT; timestamps are unix nanoseconds (INTEGER).
CREATE TABLE IF NOT EXISTS jobs (
    id                  TEXT PRIMARY KEY,
    workload            TEXT NOT NULL,
    input_uri           TEXT NOT NULL DEFAULT '',
    parameters          TEXT NOT NULL DEFAULT '{}',
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','running','reducing','completed','failed','cancelled')),
    created_at          INTEGER NOT NULL,
    completed_at        INTEGER,
    input_artifact_id   TEXT,
    result_artifact_id  TEXT,
    error_code          TEXT,
    error_message       TEXT,
    reducer_started_at  INTEGER,
    owner_id            TEXT
);

CREATE TABLE IF NOT EXISTS tasks (
    id                  TEXT PRIMARY KEY,
    job_id              TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    chunk_index         INTEGER NOT NULL,
    workload            TEXT NOT NULL,
    input_uri           TEXT,
    input_artifact_id   TEXT,
    input_sha256        TEXT NOT NULL,
    parameters          TEXT NOT NULL DEFAULT '{}',
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','leased','running','completed','failed','cancelled')),
    attempt             INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts        INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    lease_owner         TEXT,
    lease_expires_at    INTEGER,
    result_artifact_id  TEXT,
    metrics             TEXT,
    error_code          TEXT,
    error_message       TEXT,
    created_at          INTEGER NOT NULL,
    started_at          INTEGER,
    completed_at        INTEGER,
    version             INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT uq_tasks_job_chunk UNIQUE (job_id, chunk_index),
    CONSTRAINT ck_tasks_completed_result CHECK (
        status <> 'completed' OR (result_artifact_id IS NOT NULL)
    ),
    CONSTRAINT ck_tasks_leased_owner CHECK (
        status <> 'leased' OR (lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS ix_tasks_claim ON tasks (status, lease_expires_at, created_at);
CREATE INDEX IF NOT EXISTS ix_tasks_job ON tasks (job_id);

CREATE TABLE IF NOT EXISTS artifacts (
    id            TEXT PRIMARY KEY,
    job_id        TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    task_id       TEXT,
    attempt       INTEGER,
    kind          TEXT NOT NULL
                  CHECK (kind IN ('input','shard','partial_result','final_result','log')),
    filename      TEXT NOT NULL,
    storage_key   TEXT NOT NULL,
    content_type  TEXT NOT NULL,
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    sha256        TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    CONSTRAINT uq_partial_result_task_attempt UNIQUE (task_id, attempt)
);

CREATE INDEX IF NOT EXISTS ix_artifacts_job ON artifacts (job_id);

CREATE TABLE IF NOT EXISTS workers (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL,
    capabilities       TEXT NOT NULL DEFAULT '[]',
    status             TEXT NOT NULL DEFAULT 'online'
                       CHECK (status IN ('online','busy','offline')),
    owner_id           TEXT,
    trust_level        TEXT NOT NULL DEFAULT 'trusted'
                       CHECK (trust_level IN ('trusted','untrusted')),
    last_heartbeat_at  INTEGER NOT NULL,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS task_results (
    task_id             TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    owner_id            TEXT NOT NULL,
    result_sha256       TEXT NOT NULL,
    result_artifact_id  TEXT NOT NULL,
    created_at          INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (task_id, owner_id)
);

CREATE INDEX IF NOT EXISTS ix_task_results_task ON task_results (task_id);
