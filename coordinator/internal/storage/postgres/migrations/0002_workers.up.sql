BEGIN;

CREATE TYPE worker_status AS ENUM ('online','busy','offline');

-- A registered process/machine that can claim tasks. Registration returns the
-- id; liveness is tracked by last_heartbeat_at.
CREATE TABLE workers (
    id                uuid          PRIMARY KEY,
    name              text          NOT NULL DEFAULT '',
    capabilities      jsonb         NOT NULL DEFAULT '[]'::jsonb,
    status            worker_status NOT NULL DEFAULT 'online',
    last_heartbeat_at timestamptz   NOT NULL DEFAULT now(),
    created_at        timestamptz   NOT NULL DEFAULT now(),
    updated_at        timestamptz   NOT NULL DEFAULT now()
);

-- Liveness sweep: find workers that have gone quiet.
CREATE INDEX ix_workers_liveness ON workers (status, last_heartbeat_at);

COMMIT;
