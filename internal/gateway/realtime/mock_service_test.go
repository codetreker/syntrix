package realtime

import (
	"context"

	"github.com/stretchr/testify/mock"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/query"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type MockQueryService struct {
	mock.Mock
}

var _ query.Service = &MockQueryService{}

func (m *MockQueryService) GetDocument(ctx context.Context, database string, path string) (model.Document, error) {
	args := m.Called(ctx, database, path)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockQueryService) CreateDocument(ctx context.Context, database string, doc model.Document) error {
	args := m.Called(ctx, database, doc)
	return args.Error(0)
}

func (m *MockQueryService) ReplaceDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	args := m.Called(ctx, database, data, pred)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockQueryService) PatchDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	args := m.Called(ctx, database, data, pred)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockQueryService) DeleteDocument(ctx context.Context, database string, path string, pred model.Filters) error {
	args := m.Called(ctx, database, path, pred)
	return args.Error(0)
}

func (m *MockQueryService) ExecuteQuery(ctx context.Context, database string, q model.Query) ([]model.Document, error) {
	args := m.Called(ctx, database, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]model.Document), args.Error(1)
}

func (m *MockQueryService) Pull(ctx context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	args := m.Called(ctx, database, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.ReplicationPullResponse), args.Error(1)
}

func (m *MockQueryService) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	args := m.Called(ctx, database, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.ReplicationPushResponse), args.Error(1)
}

// MockStreamerService mocks streamer.Service
type MockStreamerService struct {
	mock.Mock
}

var _ streamer.Service = &MockStreamerService{}

func (m *MockStreamerService) Stream(ctx context.Context) (streamer.Stream, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(streamer.Stream), args.Error(1)
}

// MockStreamerStream mocks streamer.Stream
type MockStreamerStream struct {
	mock.Mock
}

var _ streamer.Stream = &MockStreamerStream{}

func (m *MockStreamerStream) Subscribe(ctx context.Context, database, collection string, filters []model.Filter) (streamer.Registration, error) {
	if err := ctx.Err(); err != nil {
		return streamer.Registration{}, err
	}
	args := m.Called(database, collection, filters)
	return streamer.Registration{ID: args.String(0), Generation: 1}, args.Error(1)
}

func (m *MockStreamerStream) Status() streamer.StreamStatus {
	return streamer.StreamStatus{State: streamer.StateConnected, Generation: 1}
}

func (m *MockStreamerStream) Unsubscribe(subscriptionID string) error {
	args := m.Called(subscriptionID)
	return args.Error(0)
}

func (m *MockStreamerStream) Recv() (*streamer.EventDelivery, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*streamer.EventDelivery), args.Error(1)
}

func (m *MockStreamerStream) Close() error {
	args := m.Called()
	return args.Error(0)
}

// NewTestHub creates a hub with a mock stream attached.
func NewTestHub() *Hub {
	h := NewHub()
	ms := new(MockStreamerStream)
	// Default behaviors
	ms.On("Subscribe", mock.Anything, mock.Anything, mock.Anything).Return("sub-id", nil).Maybe()
	ms.On("Unsubscribe", mock.Anything).Return(nil).Maybe()
	h.SetStream(ms)
	return h
}
