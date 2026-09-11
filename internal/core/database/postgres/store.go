package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/database"
)

// Store implements database.DatabaseStore using PostgreSQL
type Store struct {
	db        *sql.DB
	tableName string
}

// NewStore creates a new PostgreSQL-backed DatabaseStore
func NewStore(db *sql.DB, tableName string) database.DatabaseStore {
	if tableName == "" {
		tableName = "databases"
	}
	return &Store{
		db:        db,
		tableName: tableName,
	}
}

// EnsureSchema creates the databases table and indexes if they don't exist
func EnsureSchema(db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS databases (
    id                  VARCHAR(16) PRIMARY KEY,
    slug                VARCHAR(63) UNIQUE,
    display_name        VARCHAR(255) NOT NULL,
    description         TEXT,
    owner_id            VARCHAR(64) NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    max_documents       BIGINT NOT NULL DEFAULT 0,
    max_storage_bytes   BIGINT NOT NULL DEFAULT 0,
    status              VARCHAR(20) NOT NULL DEFAULT 'active',

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

CREATE INDEX IF NOT EXISTS idx_databases_owner ON databases(owner_id);
CREATE INDEX IF NOT EXISTS idx_databases_status ON databases(status);
CREATE INDEX IF NOT EXISTS idx_databases_created_at ON databases(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_databases_owner_status ON databases(owner_id, status);
CREATE INDEX IF NOT EXISTS idx_databases_slug ON databases(slug) WHERE slug IS NOT NULL;
`
	_, err := db.Exec(schema)
	return err
}

func (s *Store) Create(ctx context.Context, db *database.Database) error {
	now := time.Now()
	if db.CreatedAt.IsZero() {
		db.CreatedAt = now
	}
	if db.UpdatedAt.IsZero() {
		db.UpdatedAt = now
	}
	if db.Status == "" {
		db.Status = database.StatusActive
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO databases (
			id, slug, display_name, description, owner_id,
			created_at, updated_at, max_documents, max_storage_bytes, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		db.ID, db.Slug, db.DisplayName, db.Description, db.OwnerID,
		db.CreatedAt, db.UpdatedAt, db.MaxDocuments, db.MaxStorageBytes, db.Status,
	)

	if err != nil {
		if isUniqueViolation(err) {
			if isSlugViolation(err) {
				return database.ErrSlugExists
			}
			return database.ErrDatabaseExists
		}
		return err
	}

	return nil
}

func (s *Store) Get(ctx context.Context, id string) (*database.Database, error) {
	return s.scanDatabase(s.db.QueryRowContext(ctx, `
		SELECT id, slug, display_name, description, owner_id,
		       created_at, updated_at, max_documents, max_storage_bytes, status
		FROM databases WHERE id = $1
	`, id))
}

func (s *Store) GetBySlug(ctx context.Context, slug string) (*database.Database, error) {
	return s.scanDatabase(s.db.QueryRowContext(ctx, `
		SELECT id, slug, display_name, description, owner_id,
		       created_at, updated_at, max_documents, max_storage_bytes, status
		FROM databases WHERE slug = $1
	`, slug))
}

func (s *Store) List(ctx context.Context, opts database.ListOptions) ([]*database.Database, int, error) {
	// Set default limit
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Limit > 100 {
		opts.Limit = 100
	}

	// Build query with filters
	query := `
		SELECT id, slug, display_name, description, owner_id,
		       created_at, updated_at, max_documents, max_storage_bytes, status
		FROM databases
		WHERE ($1::VARCHAR IS NULL OR owner_id = $1)
		  AND ($2::VARCHAR IS NULL OR status = $2)
		ORDER BY created_at DESC, id
		LIMIT $3 OFFSET $4
	`

	countQuery := `
		SELECT COUNT(*)
		FROM databases
		WHERE ($1::VARCHAR IS NULL OR owner_id = $1)
		  AND ($2::VARCHAR IS NULL OR status = $2)
	`

	var ownerFilter, statusFilter interface{}
	if opts.OwnerID != "" {
		ownerFilter = opts.OwnerID
	}
	if opts.Status != "" {
		statusFilter = string(opts.Status)
	}

	// Get total count
	var total int
	err := s.db.QueryRowContext(ctx, countQuery, ownerFilter, statusFilter).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Get results
	rows, err := s.db.QueryContext(ctx, query, ownerFilter, statusFilter, opts.Limit, opts.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var databases []*database.Database
	for rows.Next() {
		db, err := s.scanDatabaseRow(rows)
		if err != nil {
			return nil, 0, err
		}
		databases = append(databases, db)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	return databases, total, nil
}

func (s *Store) Update(ctx context.Context, db *database.Database) error {
	db.UpdatedAt = time.Now()

	result, err := s.db.ExecContext(ctx, `
		UPDATE databases SET
			slug = CASE WHEN slug IS NULL THEN $2 ELSE slug END,
			display_name = $3,
			description = $4,
			status = $5,
			max_documents = $6,
			max_storage_bytes = $7,
			updated_at = $8
		WHERE id = $1
	`,
		db.ID, db.Slug, db.DisplayName, db.Description,
		db.Status, db.MaxDocuments, db.MaxStorageBytes, db.UpdatedAt,
	)

	if err != nil {
		if isUniqueViolation(err) && isSlugViolation(err) {
			return database.ErrSlugExists
		}
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return database.ErrDatabaseNotFound
	}

	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM databases WHERE id = $1`, id)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return database.ErrDatabaseNotFound
	}

	return nil
}

func (s *Store) CountByOwner(ctx context.Context, ownerID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM databases WHERE owner_id = $1
	`, ownerID).Scan(&count)
	return count, err
}

func (s *Store) Exists(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM databases WHERE id = $1 AND status = 'active')
	`, id).Scan(&exists)
	return exists, err
}

func (s *Store) Close(ctx context.Context) error {
	return nil
}

func (s *Store) scanDatabase(row *sql.Row) (*database.Database, error) {
	var db database.Database
	var description sql.NullString

	err := row.Scan(
		&db.ID, &db.Slug, &db.DisplayName, &description, &db.OwnerID,
		&db.CreatedAt, &db.UpdatedAt, &db.MaxDocuments, &db.MaxStorageBytes, &db.Status,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, database.ErrDatabaseNotFound
		}
		return nil, err
	}

	if description.Valid {
		db.Description = &description.String
	}

	return &db, nil
}

func (s *Store) scanDatabaseRow(rows *sql.Rows) (*database.Database, error) {
	var db database.Database
	var description sql.NullString

	err := rows.Scan(
		&db.ID, &db.Slug, &db.DisplayName, &description, &db.OwnerID,
		&db.CreatedAt, &db.UpdatedAt, &db.MaxDocuments, &db.MaxStorageBytes, &db.Status,
	)
	if err != nil {
		return nil, err
	}

	if description.Valid {
		db.Description = &description.String
	}

	return &db, nil
}

// isUniqueViolation checks if the error is a unique constraint violation
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// PostgreSQL error code for unique violation is 23505
	return errors.Is(err, sql.ErrNoRows) == false &&
		(contains(err.Error(), "unique constraint") ||
			contains(err.Error(), "duplicate key") ||
			contains(err.Error(), "23505"))
}

// isSlugViolation checks if the unique violation is for the slug column
func isSlugViolation(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "slug") ||
		contains(err.Error(), "databases_slug_key")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
