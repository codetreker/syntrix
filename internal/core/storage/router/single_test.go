package router

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type fakeDocumentStore struct{}

func (f *fakeDocumentStore) Get(ctx context.Context, database string, path string, opts ...types.ReadOptions) (*types.StoredDoc, error) {
	return nil, nil
}
func (f *fakeDocumentStore) Create(ctx context.Context, database string, doc types.StoredDoc) error {
	return nil
}
func (f *fakeDocumentStore) Update(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error {
	return nil
}
func (f *fakeDocumentStore) Patch(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error {
	return nil
}
func (f *fakeDocumentStore) Delete(ctx context.Context, database string, path string, pred model.Filters) error {
	return nil
}
func (f *fakeDocumentStore) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	return 0, nil
}
func (f *fakeDocumentStore) Query(ctx context.Context, database string, q model.Query) ([]*types.StoredDoc, error) {
	return nil, nil
}
func (f *fakeDocumentStore) GetMany(ctx context.Context, database string, paths []string, opts ...types.ReadOptions) ([]*types.StoredDoc, error) {
	return nil, nil
}
func (f *fakeDocumentStore) Watch(ctx context.Context, database string, collection string, after types.WatchCheckpoint, opts types.WatchOptions) (types.WatchStream, error) {
	return nil, nil
}
func (f *fakeDocumentStore) Close(ctx context.Context) error { return nil }

// Ensure indexes isn't part of DocumentStore; we call it on concrete implementations during provider init.

type fakeUserStore struct{}

func (f *fakeUserStore) CreateUser(ctx context.Context, user *types.User) error {
	return nil
}
func (f *fakeUserStore) GetUserByUsername(ctx context.Context, username string) (*types.User, error) {
	return nil, nil
}
func (f *fakeUserStore) GetUserByID(ctx context.Context, id string) (*types.User, error) {
	return nil, nil
}
func (f *fakeUserStore) UpdateUser(ctx context.Context, user *types.User) error {
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
func (f *fakeUserStore) ListUsers(ctx context.Context, limit, offset int) ([]*types.User, error) {
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

func TestSingleRouter_Selectors(t *testing.T) {
	doc := &fakeDocumentStore{}
	usr := &fakeUserStore{}
	rev := &fakeRevocationStore{}

	r := NewSingleRouter(doc, usr, rev)

	assert.Equal(t, doc, r.SelectDocument(types.OpRead))
	assert.Equal(t, doc, r.SelectDocument(types.OpWrite))
	assert.Equal(t, doc, r.SelectDocument(types.OpMigrate))

	assert.Equal(t, usr, r.SelectUser(types.OpRead))
	assert.Equal(t, usr, r.SelectUser(types.OpWrite))
	assert.Equal(t, usr, r.SelectUser(types.OpMigrate))

	assert.Equal(t, rev, r.SelectRevocation(types.OpRead))
	assert.Equal(t, rev, r.SelectRevocation(types.OpWrite))
	assert.Equal(t, rev, r.SelectRevocation(types.OpMigrate))
}

func TestSingleSplitRouters(t *testing.T) {
	doc := &fakeDocumentStore{}
	usr := &fakeUserStore{}
	rev := &fakeRevocationStore{}
	database := "default"

	rd := NewSingleDocumentRouter(doc)
	d, err := rd.Select(database, types.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, doc, d)

	ru := NewSingleUserRouter(usr)
	u, err := ru.Select(database, types.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, usr, u)

	rr := NewSingleRevocationRouter(rev)
	rv, err := rr.Select(database, types.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, rev, rv)
}
