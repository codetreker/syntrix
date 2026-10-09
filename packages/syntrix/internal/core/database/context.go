package database

import (
	"context"

	"github.com/codetreker/syntrix/internal/ctxkeys"
)

// contextKeyDatabase uses the unified context key for database
var contextKeyDatabase = ctxkeys.KeyDatabase

// WithDatabase returns a new context with the database attached
func WithDatabase(ctx context.Context, db *Database) context.Context {
	return context.WithValue(ctx, contextKeyDatabase, db)
}

// FromContext retrieves the database from the context
// Returns the database and true if found, nil and false otherwise
func FromContext(ctx context.Context) (*Database, bool) {
	db, ok := ctx.Value(contextKeyDatabase).(*Database)
	return db, ok
}

// MustFromContext retrieves the database from the context.
// IMPORTANT: Only call this after DatabaseValidationMiddleware has run.
// If called without middleware, the request handler should have checked FromContext first.
// Panics if the database is not found - this indicates a programming error.
func MustFromContext(ctx context.Context) *Database {
	db, ok := FromContext(ctx)
	if !ok {
		panic("database not found in context: middleware not applied or programming error")
	}
	return db
}
