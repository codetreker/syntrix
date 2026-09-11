package core

import (
	"context"

	"github.com/stretchr/testify/mock"
	"github.com/syntrixbase/syntrix/internal/indexer"
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
