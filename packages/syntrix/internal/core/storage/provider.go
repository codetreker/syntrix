package storage

import "context"

// Provider represents a physical connection to a storage backend.
type Provider interface {
	// Close closes the connection.
	Close(ctx context.Context) error
}
