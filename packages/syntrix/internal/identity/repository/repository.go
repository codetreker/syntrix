package repository

import (
	"context"
	"errors"
	"time"

	"github.com/codetreker/syntrix/internal/identity"
)

var (
	ErrUserNotFound = identity.ErrUserNotFound
	ErrUserExists   = identity.ErrUserExists
)

// UserRecord contains the credential-bearing persistence representation.
type UserRecord struct {
	ID            string                 `json:"id"`
	Username      string                 `json:"username"`
	PasswordHash  string                 `json:"password_hash"`
	PasswordAlgo  string                 `json:"password_algo"` // "argon2id" or "bcrypt"
	CreatedAt     time.Time              `json:"createdAt"`
	UpdatedAt     time.Time              `json:"updatedAt"`
	Disabled      bool                   `json:"disabled"`
	Roles         []string               `json:"roles"`
	DBAdmin       []string               `json:"db_admin"` // Databases with admin access
	Profile       map[string]interface{} `json:"profile"`
	LastLoginAt   time.Time              `json:"last_login_at"`
	LoginAttempts int                    `json:"login_attempts"`
	LockoutUntil  time.Time              `json:"lockout_until"`
}

// RevokedToken represents a revoked JWT
type RevokedToken struct {
	JTI       string    `bson:"_id"`
	ExpiresAt time.Time `bson:"expires_at"`
	RevokedAt time.Time `bson:"revoked_at"`
}

// UserStore defines the interface for user storage operations
type UserStore interface {
	CreateUser(ctx context.Context, user *UserRecord) error
	GetUserByUsername(ctx context.Context, username string) (*UserRecord, error)
	GetUserByID(ctx context.Context, id string) (*UserRecord, error)
	ListUsers(ctx context.Context, limit int, offset int) ([]*UserRecord, error)
	UpdateUser(ctx context.Context, user *UserRecord) error
	UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error
	EnsureIndexes(ctx context.Context) error
}

// ErrTokenAlreadyRevoked is returned when attempting to revoke an already-revoked token.
var ErrTokenAlreadyRevoked = errors.New("token already revoked")

// TokenRevocationStore defines the interface for token revocation storage operations
type TokenRevocationStore interface {
	RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error
	RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error
	// RevokeTokenIfNotRevoked atomically checks if token is revoked and revokes it.
	// Returns ErrTokenAlreadyRevoked if the token was already revoked (within grace period).
	// This prevents race conditions in concurrent token refresh attempts.
	RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error
	IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error)
	EnsureIndexes(ctx context.Context) error
}

type OpKind int

const (
	OpRead OpKind = iota
	OpWrite
	OpMigrate
)

type UserRouter interface {
	Select(string, OpKind) (UserStore, error)
}
type RevocationRouter interface {
	Select(string, OpKind) (TokenRevocationStore, error)
}
