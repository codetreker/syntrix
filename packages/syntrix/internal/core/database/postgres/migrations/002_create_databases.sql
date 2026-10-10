-- Create databases table for logical database management
CREATE TABLE IF NOT EXISTS databases (
    -- Primary identifier (auto-generated: hex(blake3(uuid())[:8]))
    id                  VARCHAR(16) PRIMARY KEY,

    -- URL-friendly identifier (optional, user-defined, immutable once set)
    slug                VARCHAR(63) UNIQUE,

    -- Display information
    display_name        VARCHAR(255) NOT NULL,
    description         TEXT,

    -- Ownership (matches auth_users.id type: VARCHAR(64))
    -- Note: No foreign key constraint - databases and auth_users may be in different PostgreSQL instances
    owner_id            VARCHAR(64) NOT NULL,

    -- Timestamps
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Quota settings (0 = unlimited)
    max_documents       BIGINT NOT NULL DEFAULT 0,
    max_storage_bytes   BIGINT NOT NULL DEFAULT 0,

    -- Status: active, suspended, deleting
    status              VARCHAR(20) NOT NULL DEFAULT 'active',

    -- Constraints
    CONSTRAINT chk_databases_id CHECK (
        id ~ '^[0-9a-f]{16}$'
    ),
    CONSTRAINT chk_databases_slug CHECK (
        slug IS NULL OR slug ~ '^[a-z][a-z0-9-]{2,62}$'
    ),
    CONSTRAINT chk_databases_status CHECK (
        status IN ('active', 'suspended', 'deleting')
    ),
    CONSTRAINT chk_databases_max_documents CHECK (max_documents >= 0),
    CONSTRAINT chk_databases_max_storage CHECK (max_storage_bytes >= 0)
);

-- Performance indexes
CREATE INDEX IF NOT EXISTS idx_databases_owner ON databases(owner_id);
CREATE INDEX IF NOT EXISTS idx_databases_status ON databases(status);
CREATE INDEX IF NOT EXISTS idx_databases_created_at ON databases(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_databases_owner_status ON databases(owner_id, status);

-- Partial index for slug lookups (only non-null slugs)
CREATE INDEX IF NOT EXISTS idx_databases_slug ON databases(slug) WHERE slug IS NOT NULL;
