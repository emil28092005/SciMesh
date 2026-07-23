-- 'running' means the worker has acknowledged start via its first heartbeat.
-- Kept in its own migration, without an explicit transaction: an enum value
-- added in a transaction cannot be USED in that same transaction, and the next
-- migration references it.
ALTER TYPE task_status ADD VALUE IF NOT EXISTS 'running';
