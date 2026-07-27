BEGIN;

-- A worker key is a long-lived credential a user creates to run a worker on
-- their own machine. Unlike the 24h login JWT, it does not expire on its own:
-- the worker presents it to /worker-tokens/exchange to mint a short-lived JWT
-- and refreshes as needed. Only a SHA-256 hash is stored, never the key itself,
-- so a database leak cannot be replayed as a credential.
CREATE TABLE worker_keys (
    id           uuid        PRIMARY KEY,
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Human label so a user can tell their machines apart when revoking.
    name         text        NOT NULL,
    -- Hex SHA-256 of the presented key. The key is high-entropy, so a fast hash
    -- is enough — no per-key salt or bcrypt cost is needed here.
    token_hash   text        NOT NULL,
    -- The leading, non-secret slice of the key, shown in the UI to identify a
    -- row without ever revealing the secret again.
    prefix       text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Last successful exchange; NULL until first use.
    last_used_at timestamptz,
    -- Set when the user revokes the key; a revoked key never exchanges again.
    revoked_at   timestamptz,

    CONSTRAINT uq_worker_keys_token_hash UNIQUE (token_hash)
);

-- Listing and revoking are always scoped to one owner's live keys.
CREATE INDEX ix_worker_keys_user_active ON worker_keys (user_id) WHERE revoked_at IS NULL;

COMMIT;
