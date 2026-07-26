BEGIN;

-- Static permission bundles. Roles rarely change, so the role→permission
-- mapping lives in code (auth middleware), not in a table. New role = deploy.
CREATE TYPE user_role AS ENUM ('user','admin');

-- One human account. The id is the stable identity that ends up in the JWT
-- `sub` claim; the coordinator stores it as jobs.owner_id.
CREATE TABLE users (
    id            uuid        PRIMARY KEY,
    -- Login handle. App lowercases before insert/lookup, so uniqueness is
    -- case-insensitive in practice.
    email         text        NOT NULL,
    -- Output of bcrypt/argon2. The salt and cost parameters are embedded in
    -- this string, so there is NO separate salt column to store.
    password_hash text        NOT NULL,
    role          user_role   NOT NULL DEFAULT 'user',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    -- No two accounts share a login.
    CONSTRAINT uq_users_email       UNIQUE (email),
    CONSTRAINT ck_users_email_lower CHECK (email = lower(email))
);

COMMIT;
