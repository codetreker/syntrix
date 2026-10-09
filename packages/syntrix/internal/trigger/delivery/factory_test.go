package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	pubsubtesting "github.com/codetreker/syntrix/internal/core/pubsub/testing"
	"github.com/codetreker/syntrix/internal/trigger/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockTaskConsumer mocks TaskConsumer
type MockTaskConsumer struct {
	mock.Mock
}

func (m *MockTaskConsumer) Start(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func TestNewService_NilConsumer(t *testing.T) {
	deps := Dependencies{
		Consumer: nil,
	}
	cfg := Config{}

	_, err := NewService(deps, cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "consumer is required")
}

func TestNewService_Success(t *testing.T) {
	mockConsumer := pubsubtesting.NewMockConsumer()

	deps := Dependencies{
		Consumer: mockConsumer,
		Metrics:  &types.NoopMetrics{},
	}
	cfg := Config{
		NumWorkers: 4,
	}

	svc, err := NewService(deps, cfg)
	assert.NoError(t, err)
	assert.NotNil(t, svc)
}

func TestNewService_WithDefaults(t *testing.T) {
	mockConsumer := pubsubtesting.NewMockConsumer()

	deps := Dependencies{
		Consumer: mockConsumer,
	}
	cfg := Config{} // No options - should use defaults

	svc, err := NewService(deps, cfg)
	assert.NoError(t, err)
	assert.NotNil(t, svc)

	// The service should be created with default NumWorkers (16)
	s := svc.(*service)
	nc := s.consumer.(*natsConsumer)
	assert.Equal(t, 16, nc.numWorkers)
}

func TestNewService_WithAllOptions(t *testing.T) {
	mockConsumer := pubsubtesting.NewMockConsumer()

	deps := Dependencies{
		Consumer: mockConsumer,
		Metrics:  &types.NoopMetrics{},
	}
	cfg := Config{
		NumWorkers:      8,
		ChannelBufSize:  100,
		DrainTimeout:    5 * time.Second,
		ShutdownTimeout: 10 * time.Second,
	}

	svc, err := NewService(deps, cfg)
	assert.NoError(t, err)
	assert.NotNil(t, svc)

	// Verify options were applied
	s := svc.(*service)
	nc := s.consumer.(*natsConsumer)
	assert.Equal(t, 8, nc.numWorkers)
	assert.Equal(t, 100, nc.channelBufSize)
	assert.Equal(t, 5*time.Second, nc.drainTimeout)
	assert.Equal(t, 10*time.Second, nc.shutdownTimeout)
}

func TestService_Start(t *testing.T) {
	mockConsumer := new(MockTaskConsumer)
	svc := &service{
		consumer: mockConsumer,
	}

	mockConsumer.On("Start", mock.Anything).Return(nil)

	err := svc.Start(context.Background())
	assert.NoError(t, err)
	mockConsumer.AssertExpectations(t)
}

func TestService_Start_Error(t *testing.T) {
	mockConsumer := new(MockTaskConsumer)
	svc := &service{
		consumer: mockConsumer,
	}

	mockConsumer.On("Start", mock.Anything).Return(errors.New("consumer error"))

	err := svc.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "consumer error")
	mockConsumer.AssertExpectations(t)
}
