package router

import (
	"context"
	"time"

	"github.com/codetreker/syntrix/internal/identity/repository"
)

// RoutedUserStore implements UserStore by routing operations
type RoutedUserStore struct {
	router repository.UserRouter
}

func NewRoutedUserStore(router repository.UserRouter) repository.UserStore {
	return &RoutedUserStore{router: router}
}

func (s *RoutedUserStore) CreateUser(ctx context.Context, user *repository.UserRecord) error {
	// Global user creation - use default database for routing
	store, err := s.router.Select("default", repository.OpWrite)
	if err != nil {
		return err
	}
	return store.CreateUser(ctx, user)
}

func (s *RoutedUserStore) GetUserByUsername(ctx context.Context, username string) (*repository.UserRecord, error) {
	// Global user lookup - use default database for routing
	store, err := s.router.Select("default", repository.OpRead)
	if err != nil {
		return nil, err
	}
	return store.GetUserByUsername(ctx, username)
}

func (s *RoutedUserStore) GetUserByID(ctx context.Context, id string) (*repository.UserRecord, error) {
	// For global user lookup by ID, we use default database routing
	store, err := s.router.Select("default", repository.OpRead)
	if err != nil {
		return nil, err
	}
	return store.GetUserByID(ctx, id)
}

func (s *RoutedUserStore) ListUsers(ctx context.Context, limit int, offset int) ([]*repository.UserRecord, error) {
	// Global user list operation
	store, err := s.router.Select("default", repository.OpRead)
	if err != nil {
		return nil, err
	}
	return store.ListUsers(ctx, limit, offset)
}

func (s *RoutedUserStore) UpdateUser(ctx context.Context, user *repository.UserRecord) error {
	// Global user update operation
	store, err := s.router.Select("default", repository.OpWrite)
	if err != nil {
		return err
	}
	return store.UpdateUser(ctx, user)
}

func (s *RoutedUserStore) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	// Global user update - use default database for routing
	store, err := s.router.Select("default", repository.OpWrite)
	if err != nil {
		return err
	}
	return store.UpdateUserLoginStats(ctx, id, lastLogin, attempts, lockoutUntil)
}

func (s *RoutedUserStore) EnsureIndexes(ctx context.Context) error {
	// Index initialization targets the default write source.
	store, err := s.router.Select("default", repository.OpWrite)
	if err != nil {
		return err
	}
	return store.EnsureIndexes(ctx)
}

// RoutedRevocationStore implements TokenRevocationStore by routing operations
type RoutedRevocationStore struct {
	router repository.RevocationRouter
}

func NewRoutedRevocationStore(router repository.RevocationRouter) repository.TokenRevocationStore {
	return &RoutedRevocationStore{router: router}
}

func (s *RoutedRevocationStore) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	// Global revocation - use default database for routing
	store, err := s.router.Select("default", repository.OpWrite)
	if err != nil {
		return err
	}
	return store.RevokeToken(ctx, jti, expiresAt)
}

func (s *RoutedRevocationStore) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	// Global revocation - use default database for routing
	store, err := s.router.Select("default", repository.OpWrite)
	if err != nil {
		return err
	}
	return store.RevokeTokenImmediate(ctx, jti, expiresAt)
}

func (s *RoutedRevocationStore) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	// Global revocation - use default database for routing
	store, err := s.router.Select("default", repository.OpWrite)
	if err != nil {
		return err
	}
	return store.RevokeTokenIfNotRevoked(ctx, jti, expiresAt, gracePeriod)
}

func (s *RoutedRevocationStore) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	// Global revocation check - use default database for routing
	store, err := s.router.Select("default", repository.OpRead)
	if err != nil {
		return false, err
	}
	return store.IsRevoked(ctx, jti, gracePeriod)
}

func (s *RoutedRevocationStore) EnsureIndexes(ctx context.Context) error {
	store, err := s.router.Select("default", repository.OpWrite)
	if err != nil {
		return err
	}
	return store.EnsureIndexes(ctx)
}
