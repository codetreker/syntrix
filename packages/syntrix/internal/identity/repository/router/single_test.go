package router

import (
	"context"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/stretchr/testify/assert"
)

type fakeUserStore struct{}

func (f *fakeUserStore) CreateUser(ctx context.Context, user *repository.UserRecord) error {
	return nil
}
func (f *fakeUserStore) GetUserByUsername(ctx context.Context, username string) (*repository.UserRecord, error) {
	return nil, nil
}
func (f *fakeUserStore) GetUserByID(ctx context.Context, id string) (*repository.UserRecord, error) {
	return nil, nil
}
func (f *fakeUserStore) UpdateUser(ctx context.Context, user *repository.UserRecord) error {
	return nil
}
func (f *fakeUserStore) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	return nil
}
func (f *fakeUserStore) UpdateUserPassword(ctx context.Context, userID string, hashedPassword string) error {
	return nil
}
func (f *fakeUserStore) UpdateUserRoles(ctx context.Context, userID string, roles []string) error {
	return nil
}
func (f *fakeUserStore) DeleteUser(ctx context.Context, id string) error {
	return nil
}
func (f *fakeUserStore) ListUsers(ctx context.Context, limit, offset int) ([]*repository.UserRecord, error) {
	return nil, nil
}
func (f *fakeUserStore) EnsureIndexes(ctx context.Context) error { return nil }
func (f *fakeUserStore) Close(ctx context.Context) error         { return nil }

// Fake revocation store

type fakeRevocationStore struct{}

func (f *fakeRevocationStore) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	return nil
}
func (f *fakeRevocationStore) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	return nil
}
func (f *fakeRevocationStore) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	return false, nil
}
func (f *fakeRevocationStore) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	return nil
}
func (f *fakeRevocationStore) EnsureIndexes(ctx context.Context) error { return nil }
func (f *fakeRevocationStore) Close(ctx context.Context) error         { return nil }

func TestSingleSplitRouters(t *testing.T) {
	usr := &fakeUserStore{}
	rev := &fakeRevocationStore{}
	database := "default"

	ru := NewSingleUserRouter(usr)
	u, err := ru.Select(database, repository.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, usr, u)

	rr := NewSingleRevocationRouter(rev)
	rv, err := rr.Select(database, repository.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, rev, rv)
}
