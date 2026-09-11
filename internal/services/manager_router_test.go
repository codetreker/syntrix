package services

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/syntrixbase/syntrix/internal/config"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// Mocks
type mockDocumentProvider struct {
	mock.Mock
}

func (m *mockDocumentProvider) Document() storage.DocumentStore {
	args := m.Called()
	return args.Get(0).(storage.DocumentStore)
}

func (m *mockDocumentProvider) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

type mockAuthProvider struct {
	mock.Mock
}

func (m *mockAuthProvider) Users() storage.UserStore {
	args := m.Called()
	return args.Get(0).(storage.UserStore)
}

func (m *mockAuthProvider) Revocations() storage.TokenRevocationStore {
	args := m.Called()
	return args.Get(0).(storage.TokenRevocationStore)
}

func (m *mockAuthProvider) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

type mockDocumentStore struct {
	mock.Mock
}

func (m *mockDocumentStore) Get(ctx context.Context, database, path string, _ ...types.ReadOptions) (*types.StoredDoc, error) {
	args := m.Called(ctx, database, path)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.StoredDoc), args.Error(1)
}
func (m *mockDocumentStore) Create(ctx context.Context, database string, doc types.StoredDoc) error {
	return m.Called(ctx, database, doc).Error(0)
}
func (m *mockDocumentStore) Update(ctx context.Context, database, path string, data map[string]interface{}, pred model.Filters) error {
	return m.Called(ctx, database, path, data, pred).Error(0)
}
func (m *mockDocumentStore) Patch(ctx context.Context, database, path string, data map[string]interface{}, pred model.Filters) error {
	return m.Called(ctx, database, path, data, pred).Error(0)
}
func (m *mockDocumentStore) Delete(ctx context.Context, database, path string, pred model.Filters) error {
	return m.Called(ctx, database, path, pred).Error(0)
}
func (m *mockDocumentStore) Query(ctx context.Context, database string, q model.Query) ([]*types.StoredDoc, error) {
	args := m.Called(ctx, database, q)
	return args.Get(0).([]*types.StoredDoc), args.Error(1)
}
func (m *mockDocumentStore) GetMany(ctx context.Context, database string, paths []string, _ ...types.ReadOptions) ([]*types.StoredDoc, error) {
	args := m.Called(ctx, database, paths)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*types.StoredDoc), args.Error(1)
}
func (m *mockDocumentStore) Watch(ctx context.Context, database, collection string, after types.WatchCheckpoint, opts types.WatchOptions) (types.WatchStream, error) {
	args := m.Called(ctx, database, collection, after, opts)
	return args.Get(0).(types.WatchStream), args.Error(1)
}
func (m *mockDocumentStore) Close(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *mockDocumentStore) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	args := m.Called(ctx, database, limit)
	return args.Int(0), args.Error(1)
}

type mockUserStore struct {
	mock.Mock
}

func (m *mockUserStore) CreateUser(ctx context.Context, user *types.User) error {
	return nil
}
func (m *mockUserStore) GetUserByUsername(ctx context.Context, username string) (*types.User, error) {
	return nil, nil
}
func (m *mockUserStore) GetUserByID(ctx context.Context, id string) (*types.User, error) {
	return nil, nil
}
func (m *mockUserStore) ListUsers(ctx context.Context, limit int, offset int) ([]*types.User, error) {
	return nil, nil
}
func (m *mockUserStore) UpdateUser(ctx context.Context, user *types.User) error {
	return nil
}
func (m *mockUserStore) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	return nil
}
func (m *mockUserStore) EnsureIndexes(ctx context.Context) error {
	return nil
}
func (m *mockUserStore) Close(ctx context.Context) error {
	return nil
}

type mockRevocationStore struct {
	mock.Mock
}

func (m *mockRevocationStore) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	return nil
}
func (m *mockRevocationStore) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	return nil
}
func (m *mockRevocationStore) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	return false, nil
}
func (m *mockRevocationStore) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	return nil
}
func (m *mockRevocationStore) EnsureIndexes(ctx context.Context) error {
	return nil
}
func (m *mockRevocationStore) Close(ctx context.Context) error {
	return nil
}

func TestManager_Init_RouterWiring(t *testing.T) {
	// Save original factories and restore after test
	origFactory := storageFactoryFactory
	defer func() {
		storageFactoryFactory = origFactory
	}()

	// Setup mocks
	mockDocStore := new(mockDocumentStore)
	mockUsrStore := new(mockUserStore)
	mockRevStore := new(mockRevocationStore)

	// Override factories
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: mockDocStore,
			usrStore: mockUsrStore,
			revStore: mockRevStore,
		}, nil
	}

	// Initialize Manager
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunQuery: true}) // RunQuery triggers initStorage

	err := mgr.Init(context.Background())
	assert.NoError(t, err)

	// Verify Stores are initialized and wired correctly
	assert.NotNil(t, mgr.storageFactory.Document())
	assert.NotNil(t, mgr.storageFactory.User())
	assert.NotNil(t, mgr.storageFactory.Revocation())

	// Verify Stores route to the mocked stores
	// Since we use RoutedStore, we can't directly compare equality of the store object itself easily
	// without exposing the inner router. But we can verify behavior or check if it's not nil.
	// For now, just checking not nil is a basic check.
	// To be more rigorous, we could call a method and see if it hits the mock.

	mockDocStore.On("Get", mock.Anything, "default", "test").Return(&types.StoredDoc{}, nil)
	_, _ = mgr.storageFactory.Document().Get(context.Background(), "default", "test")
	mockDocStore.AssertCalled(t, "Get", mock.Anything, "default", "test")
}
