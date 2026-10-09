package database

import (
	"errors"
	"time"
)

// Database represents a logical database in the system
type Database struct {
	ID              string         `json:"id" db:"id"`
	Slug            *string        `json:"slug" db:"slug"`
	DisplayName     string         `json:"display_name" db:"display_name"`
	Description     *string        `json:"description,omitempty" db:"description"`
	OwnerID         string         `json:"owner_id" db:"owner_id"`
	CreatedAt       time.Time      `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at" db:"updated_at"`
	MaxDocuments    int64          `json:"max_documents" db:"max_documents"`
	MaxStorageBytes int64          `json:"max_storage_bytes" db:"max_storage_bytes"`
	Status          DatabaseStatus `json:"status" db:"status"`
}

// DatabaseStatus represents the status of a database
type DatabaseStatus string

const (
	StatusActive    DatabaseStatus = "active"
	StatusSuspended DatabaseStatus = "suspended"
	StatusDeleting  DatabaseStatus = "deleting"
)

// IsValid checks if the status is a valid DatabaseStatus
func (s DatabaseStatus) IsValid() bool {
	switch s {
	case StatusActive, StatusSuspended, StatusDeleting:
		return true
	default:
		return false
	}
}

// DatabaseSettings contains quota settings for a database
type DatabaseSettings struct {
	MaxDocuments    int64 `json:"max_documents"`
	MaxStorageBytes int64 `json:"max_storage_bytes"`
}

// Error definitions for database operations
var (
	ErrDatabaseNotFound    = errors.New("database not found")
	ErrDatabaseExists      = errors.New("database already exists")
	ErrDatabaseSuspended   = errors.New("database is suspended")
	ErrDatabaseDeleting    = errors.New("database is being deleted")
	ErrProtectedDatabase   = errors.New("cannot delete protected database")
	ErrNotOwner            = errors.New("not the owner of this database")
	ErrQuotaExceeded       = errors.New("database quota exceeded")
	ErrReservedSlug        = errors.New("slug is reserved")
	ErrInvalidSlugFormat   = errors.New("invalid slug format")
	ErrInvalidSlugLength   = errors.New("slug must be 3-63 characters")
	ErrSlugImmutable       = errors.New("slug cannot be changed once set")
	ErrSlugExists          = errors.New("slug already exists")
	ErrInvalidDatabaseID   = errors.New("invalid database ID format")
	ErrInvalidStatus       = errors.New("invalid database status")
	ErrDisplayNameRequired = errors.New("display name is required")
)

// ProtectedSlugs contains slugs that cannot be deleted
var ProtectedSlugs = map[string]bool{
	"default": true,
}

// IsProtected returns true if the database cannot be deleted
func (d *Database) IsProtected() bool {
	if d.Slug == nil {
		return false
	}
	return ProtectedSlugs[*d.Slug]
}

// Settings returns the database settings as a DatabaseSettings struct
func (d *Database) Settings() DatabaseSettings {
	return DatabaseSettings{
		MaxDocuments:    d.MaxDocuments,
		MaxStorageBytes: d.MaxStorageBytes,
	}
}
