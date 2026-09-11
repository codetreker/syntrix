package query

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// MockIndexerService is a mock implementation of indexer.Service
type MockIndexerService struct {
	mock.Mock
}

func (m *MockIndexerService) Search(ctx context.Context, database string, plan indexer.Plan) ([]indexer.DocRef, error) {
	args := m.Called(ctx, database, plan)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]indexer.DocRef), args.Error(1)
}

func (m *MockIndexerService) Health(ctx context.Context) (indexer.Health, error) {
	args := m.Called(ctx)
	return args.Get(0).(indexer.Health), args.Error(1)
}

func (m *MockIndexerService) Stats(ctx context.Context) (indexer.Stats, error) {
	args := m.Called(ctx)
	return args.Get(0).(indexer.Stats), args.Error(1)
}

// MockDocumentStore is a mock implementation of storage.DocumentStore
type MockDocumentStore struct {
	mock.Mock
}

func (m *MockDocumentStore) Get(ctx context.Context, database, path string, _ ...storage.ReadOptions) (*storage.StoredDoc, error) {
	args := m.Called(ctx, database, path)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.StoredDoc), args.Error(1)
}

func (m *MockDocumentStore) Create(ctx context.Context, database string, doc storage.StoredDoc) error {
	args := m.Called(ctx, database, doc)
	return args.Error(0)
}

func (m *MockDocumentStore) Update(ctx context.Context, database, path string, data map[string]interface{}, pred model.Filters) error {
	args := m.Called(ctx, database, path, data, pred)
	return args.Error(0)
}

func (m *MockDocumentStore) Patch(ctx context.Context, database, path string, data map[string]interface{}, pred model.Filters) error {
	args := m.Called(ctx, database, path, data, pred)
	return args.Error(0)
}

func (m *MockDocumentStore) Delete(ctx context.Context, database, path string, pred model.Filters) error {
	args := m.Called(ctx, database, path, pred)
	return args.Error(0)
}

func (m *MockDocumentStore) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	args := m.Called(ctx, database, limit)
	return args.Int(0), args.Error(1)
}

func (m *MockDocumentStore) Query(ctx context.Context, database string, q model.Query) ([]*storage.StoredDoc, error) {
	args := m.Called(ctx, database, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*storage.StoredDoc), args.Error(1)
}

func (m *MockDocumentStore) GetMany(ctx context.Context, database string, paths []string, opts ...storage.ReadOptions) ([]*storage.StoredDoc, error) {
	args := m.Called(ctx, database, paths)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*storage.StoredDoc), args.Error(1)
}

func (m *MockDocumentStore) Watch(ctx context.Context, database, collection string, after storage.WatchCheckpoint, opts storage.WatchOptions) (storage.WatchStream, error) {
	args := m.Called(ctx, database, collection, after, opts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(storage.WatchStream), args.Error(1)
}

func (m *MockDocumentStore) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func TestNewService(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockIndexer := new(MockIndexerService)
	service := NewService(mockStore, mockIndexer)

	assert.NotNil(t, service)
	// Verify that it implements the Service interface
	var _ Service = service
}

func TestNewClient(t *testing.T) {
	client, err := NewClient("localhost:50051")

	assert.NoError(t, err)
	assert.NotNil(t, client)
	// Verify that it implements the Service interface
	var _ Service = client
}

func TestNewGRPCServer(t *testing.T) {
	mockStore := new(MockDocumentStore)
	mockIndexer := new(MockIndexerService)
	service := NewService(mockStore, mockIndexer)
	grpcServer := NewGRPCServer(service)

	assert.NotNil(t, grpcServer)
	// Verify that it implements pb.QueryServiceServer
	var _ pb.QueryServiceServer = grpcServer
}
