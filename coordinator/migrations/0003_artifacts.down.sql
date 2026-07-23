BEGIN;

ALTER TABLE tasks DROP COLUMN IF EXISTS input_artifact_id;
ALTER TABLE tasks DROP COLUMN IF EXISTS result_artifact_id;
ALTER TABLE jobs  DROP COLUMN IF EXISTS input_artifact_id;
ALTER TABLE jobs  DROP COLUMN IF EXISTS result_artifact_id;

DROP TABLE IF EXISTS artifacts;
DROP TYPE IF EXISTS artifact_kind;

COMMIT;
