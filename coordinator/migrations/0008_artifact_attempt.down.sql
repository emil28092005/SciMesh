BEGIN;

ALTER TABLE artifacts DROP CONSTRAINT IF EXISTS ck_artifact_attempt_positive;
ALTER TABLE artifacts DROP CONSTRAINT IF EXISTS ck_partial_result_attempt;
ALTER TABLE artifacts DROP COLUMN IF EXISTS attempt;

COMMIT;
