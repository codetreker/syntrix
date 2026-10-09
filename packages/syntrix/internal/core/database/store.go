package database

import "context"

// ListOptions contains options for listing databases
type ListOptions struct {
	// OwnerID filters by owner (empty means no filter)
	OwnerID string
	// Status filters by status (empty means no filter)
	Status DatabaseStatus
	// Limit is the maximum number of results to return (0 means default limit)
	Limit int
	// Offset is the number of results to skip
	Offset int
}

// DatabaseStore defines the interface for database metadata storage operations
type DatabaseStore interface {
	// Create creates a new database record
	Create(ctx context.Context, db *Database) error

	// Get retrieves a database by its ID
	Get(ctx context.Context, id string) (*Database, error)

	// GetBySlug retrieves a database by its slug
	GetBySlug(ctx context.Context, slug string) (*Database, error)

	// List retrieves databases with optional filtering
	// Returns the list of databases and total count
	List(ctx context.Context, opts ListOptions) ([]*Database, int, error)

	// Update updates an existing database record
	Update(ctx context.Context, db *Database) error

	// Delete permanently removes a database record (hard delete)
	Delete(ctx context.Context, id string) error

	// CountByOwner counts databases owned by a specific user
	CountByOwner(ctx context.Context, ownerID string) (int, error)

	// Exists checks if a database with the given ID exists
	Exists(ctx context.Context, id string) (bool, error)

	// Close closes any underlying connections
	Close(ctx context.Context) error
}
