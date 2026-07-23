BEGIN;

-- Inputs can now arrive as uploaded artifacts (POST /jobs/upload), not only as
-- external URIs. Relax the URI requirement and require every task to have an
-- input one way or the other.
ALTER TABLE jobs  ALTER COLUMN input_uri DROP NOT NULL;
ALTER TABLE tasks ALTER COLUMN input_uri DROP NOT NULL;

ALTER TABLE tasks ADD CONSTRAINT ck_tasks_has_input CHECK (
    input_uri IS NOT NULL OR input_artifact_id IS NOT NULL
);

COMMIT;
