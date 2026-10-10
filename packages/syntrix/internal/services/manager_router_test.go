package services

import (
	"context"
	"testing"

	"github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

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

func TestManager_Init_RouterWiring(t *testing.T) {
	setupManagerFactories(t)
	mockDocStore := new(mockDocumentStore)
	shared := &storage.Backends{}
	backendCalls := 0
	storageBackendsFactory = func(context.Context, *config.Config) (*storage.Backends, error) {
		backendCalls++
		return shared, nil
	}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config, backends *storage.Backends) (storage.StorageFactory, error) {
		assert.Same(t, shared, backends)
		return &fakeStorageFactory{docStore: mockDocStore}, nil
	}
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunQuery: true})
	defer mgr.Shutdown(context.Background())
	assert.NoError(t, mgr.Init(context.Background()))
	assert.Equal(t, 1, backendCalls)
	assert.Nil(t, mgr.identityModule)
	mockDocStore.On("Get", mock.Anything, "default", "test").Return(&types.StoredDoc{}, nil)
	_, err := mgr.storageFactory.Document().Get(context.Background(), "default", "test")
	assert.NoError(t, err)
	mockDocStore.AssertCalled(t, "Get", mock.Anything, "default", "test")
}
