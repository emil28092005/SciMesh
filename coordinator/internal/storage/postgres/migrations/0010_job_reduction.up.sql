-- PostgreSQL enum values must be committed before they are used by a later
-- transaction, so this migration intentionally has no BEGIN/COMMIT wrapper.
ALTER TYPE job_status ADD VALUE IF NOT EXISTS 'reducing';

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS error_code text;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS error_message text;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS reducer_started_at timestamptz;
