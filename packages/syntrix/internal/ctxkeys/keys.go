// Package ctxkeys provides unified context keys for the application.
// This package consolidates all context keys to avoid duplication and ensure consistency.
package ctxkeys

// Key is the type for all context keys in the application.
// Using a dedicated type prevents collisions with keys from other packages.
type Key string

const (
	// Request-scoped keys
	KeyRequestID Key = "request_id"
	KeyDatabase  Key = "database"

	// Auth-scoped keys
	KeyUserID   Key = "user_id"
	KeyUsername Key = "username"
	KeyRoles    Key = "roles"
	KeyClaims   Key = "claims"
	KeyDBAdmin  Key = "db_admin"

	// Handler-scoped keys
	KeyParsedBody Key = "parsed_body"
)

// Backward compatibility aliases for existing code.
// These will be deprecated in a future version.
// New code should use the Key constants directly.
var (
	// Deprecated: Use KeyRequestID instead
	ContextKeyRequestID = KeyRequestID
	// Deprecated: Use KeyDatabase instead
	ContextKeyDatabase = KeyDatabase
	// Deprecated: Use KeyUserID instead
	ContextKeyUserID = KeyUserID
	// Deprecated: Use KeyUsername instead
	ContextKeyUsername = KeyUsername
	// Deprecated: Use KeyRoles instead
	ContextKeyRoles = KeyRoles
	// Deprecated: Use KeyClaims instead
	ContextKeyClaims = KeyClaims
	// Deprecated: Use KeyDBAdmin instead
	ContextKeyDBAdmin = KeyDBAdmin
	// Deprecated: Use KeyParsedBody instead
	ContextKeyParsedBody = KeyParsedBody
)
