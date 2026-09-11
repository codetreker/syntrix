package core

import (
	"context"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/stretchr/testify/mock"
)

// MockStorageBackend is a mock implementation of storage.StorageBackend
type MockStorageBackend struct {
	mock.Mock
}

func (m *MockStorageBackend) Get(ctx context.Context, database, path string, opts ...storage.ReadOptions) (*storage.StoredDoc, error) {
	args := m.Called(ctx, database, path)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.StoredDoc), args.Error(1)
}

type routedReadStorage struct {
	MockStorageBackend
}

func (m *routedReadStorage) Get(ctx context.Context, database, path string, opts ...storage.ReadOptions) (*storage.StoredDoc, error) {
	args := m.Called(ctx, database, path, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.StoredDoc), args.Error(1)
}

func (m *MockStorageBackend) Create(ctx context.Context, database string, doc storage.StoredDoc) error {
	args := m.Called(ctx, database, doc)
	return args.Error(0)
}

func (m *MockStorageBackend) Update(ctx context.Context, database, path string, data map[string]interface{}, pred model.Filters) error {
	args := m.Called(ctx, database, path, data, pred)
	return args.Error(0)
}

func (m *MockStorageBackend) Patch(ctx context.Context, database, path string, data map[string]interface{}, pred model.Filters) error {
	args := m.Called(ctx, database, path, data, pred)
	return args.Error(0)
}

func (m *MockStorageBackend) Delete(ctx context.Context, database, path string, pred model.Filters) error {
	args := m.Called(ctx, database, path, pred)
	return args.Error(0)
}

func (m *MockStorageBackend) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	args := m.Called(ctx, database, limit)
	return args.Int(0), args.Error(1)
}

func (m *MockStorageBackend) Query(ctx context.Context, database string, q model.Query) ([]*storage.StoredDoc, error) {
	args := m.Called(ctx, database, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*storage.StoredDoc), args.Error(1)
}

func (m *MockStorageBackend) GetMany(ctx context.Context, database string, paths []string, opts ...storage.ReadOptions) ([]*storage.StoredDoc, error) {
	args := m.Called(ctx, database, paths)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*storage.StoredDoc), args.Error(1)
}

func (m *MockStorageBackend) Watch(ctx context.Context, database, collection string, after storage.WatchCheckpoint, opts storage.WatchOptions) (storage.WatchStream, error) {
	args := m.Called(ctx, database, collection, after, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(storage.WatchStream), args.Error(1)
}

func (m *MockStorageBackend) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// MockCSPService is a mock implementation of csp.Service
type MockCSPService struct {
	mock.Mock
}

func (m *MockCSPService) Watch(ctx context.Context, database, collection string, after storage.WatchCheckpoint, opts storage.WatchOptions) (storage.WatchStream, error) {
	args := m.Called(ctx, database, collection, after, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(storage.WatchStream), args.Error(1)
}
