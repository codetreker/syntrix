-- Create users table for authentication
CREATE TABLE IF NOT EXISTS auth_users (
    id              VARCHAR(64) PRIMARY KEY,
    username        VARCHAR(255) NOT NULL,
    password_hash   TEXT NOT NULL,
    password_algo   VARCHAR(32) NOT NULL DEFAULT 'argon2id',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    roles           TEXT[] NOT NULL DEFAULT '{}',
    db_admin        TEXT[] NOT NULL DEFAULT '{}',
    profile         JSONB NOT NULL DEFAULT '{}',
    last_login_at   TIMESTAMPTZ,
    login_attempts  INTEGER NOT NULL DEFAULT 0,
    lockout_until   TIMESTAMPTZ
);

-- Unique index on username (globally unique)
CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_users_username ON auth_users(username);

-- Partial index for disabled users (efficient lookup of disabled accounts)
CREATE INDEX IF NOT EXISTS idx_auth_users_disabled ON auth_users(disabled) WHERE disabled = true;
