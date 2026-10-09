package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	pubsubtesting "github.com/codetreker/syntrix/internal/core/pubsub/testing"
	"github.com/codetreker/syntrix/internal/trigger/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// --- Mocks ---

type MockWorker struct {
	mock.Mock
}

func (m *MockWorker) ProcessTask(ctx context.Context, task *types.DeliveryTask) error {
	args := m.Called(ctx, task)
	return args.Error(0)
}

// --- Tests ---

func TestNewTaskConsumer(t *testing.T) {
	mockConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	c := NewTaskConsumer(mockConsumer, mockWorker, 4, nil)
	assert.NotNil(t, c)

	nc := c.(*natsConsumer)
	assert.Equal(t, 4, nc.numWorkers)
	assert.Equal(t, DefaultChannelBufferSize, nc.channelBufSize)
}

func TestNewTaskConsumer_DefaultNumWorkers(t *testing.T) {
	mockConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	// Test with numWorkers = 0, should default to 16
	c := NewTaskConsumer(mockConsumer, mockWorker, 0, nil)
	nc := c.(*natsConsumer)
	assert.Equal(t, 16, nc.numWorkers)

	// Test with numWorkers = -1, should also default to 16
	c2 := NewTaskConsumer(mockConsumer, mockWorker, -1, nil)
	nc2 := c2.(*natsConsumer)
	assert.Equal(t, 16, nc2.numWorkers)
}

func TestWithChannelBufferSize(t *testing.T) {
	mockConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	c := NewTaskConsumer(mockConsumer, mockWorker, 1, nil, WithChannelBufferSize(200))
	nc := c.(*natsConsumer)
	assert.Equal(t, 200, nc.channelBufSize)
}

func TestWithDrainTimeout(t *testing.T) {
	mockConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	c := NewTaskConsumer(mockConsumer, mockWorker, 1, nil, WithDrainTimeout(10*time.Second))
	nc := c.(*natsConsumer)
	assert.Equal(t, 10*time.Second, nc.drainTimeout)
}

func TestWithShutdownTimeout(t *testing.T) {
	mockConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	c := NewTaskConsumer(mockConsumer, mockWorker, 1, nil, WithShutdownTimeout(20*time.Second))
	nc := c.(*natsConsumer)
	assert.Equal(t, 20*time.Second, nc.shutdownTimeout)
}

func TestConsumer_Start_Success(t *testing.T) {
	mockPubsubConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	c := NewTaskConsumer(mockPubsubConsumer, mockWorker, 1, nil,
		WithDrainTimeout(100*time.Millisecond),
		WithShutdownTimeout(200*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())

	// Setup worker expectation
	mockWorker.On("ProcessTask", mock.Anything, mock.MatchedBy(func(t *types.DeliveryTask) bool {
		return t.TriggerID == "t1"
	})).Return(nil)

	// Start consumer in goroutine
	done := make(chan error)
	go func() {
		done <- c.Start(ctx)
	}()

	// Wait for consumer to start
	time.Sleep(50 * time.Millisecond)

	// Send a message
	task := &types.DeliveryTask{TriggerID: "t1", Collection: "col1", DocumentID: "doc1"}
	taskBytes, _ := json.Marshal(task)
	msg := pubsubtesting.NewMockMessage("test.subject", taskBytes)
	mockPubsubConsumer.Send(msg)

	// Wait for processing
	time.Sleep(50 * time.Millisecond)

	// Cancel context to stop
	cancel()

	// Wait for Start to return
	err := <-done
	assert.NoError(t, err)

	// Verify message was acked
	assert.True(t, msg.IsAcked())
	mockWorker.AssertExpectations(t)
}

func TestConsumer_Start_SubscribeError(t *testing.T) {
	mockPubsubConsumer := pubsubtesting.NewMockConsumer()
	mockPubsubConsumer.SetError(errors.New("subscribe failed"))
	mockWorker := new(MockWorker)

	c := NewTaskConsumer(mockPubsubConsumer, mockWorker, 1, nil)

	err := c.Start(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to subscribe")
}

func TestConsumer_Start_ProcessError(t *testing.T) {
	mockPubsubConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	c := NewTaskConsumer(mockPubsubConsumer, mockWorker, 1, nil,
		WithDrainTimeout(100*time.Millisecond),
		WithShutdownTimeout(200*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())

	// Setup worker to return error
	mockWorker.On("ProcessTask", mock.Anything, mock.Anything).Return(errors.New("process failed"))

	// Start consumer in goroutine
	done := make(chan error)
	go func() {
		done <- c.Start(ctx)
	}()

	// Wait for consumer to start
	time.Sleep(50 * time.Millisecond)

	// Send a message
	task := &types.DeliveryTask{
		TriggerID:  "t1",
		Collection: "col1",
		DocumentID: "doc1",
		RetryPolicy: types.RetryPolicy{
			MaxAttempts:    3,
			InitialBackoff: types.Duration(1 * time.Second),
		},
	}
	taskBytes, _ := json.Marshal(task)
	msg := pubsubtesting.NewMockMessage("test.subject", taskBytes)
	msg.SetMetadata(pubsub.MessageMetadata{NumDelivered: 1})
	mockPubsubConsumer.Send(msg)

	// Wait for processing
	time.Sleep(50 * time.Millisecond)

	cancel()
	<-done

	// Verify message was nak'd with delay (retry)
	assert.True(t, msg.IsNaked())
	assert.Equal(t, 1*time.Second, msg.NakDelay())
}

func TestConsumer_Start_InvalidPayload(t *testing.T) {
	mockPubsubConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	c := NewTaskConsumer(mockPubsubConsumer, mockWorker, 1, nil,
		WithDrainTimeout(100*time.Millisecond),
		WithShutdownTimeout(200*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())

	// Start consumer in goroutine
	done := make(chan error)
	go func() {
		done <- c.Start(ctx)
	}()

	// Wait for consumer to start
	time.Sleep(50 * time.Millisecond)

	// Send invalid message
	msg := pubsubtesting.NewMockMessage("test.subject", []byte("invalid-json"))
	mockPubsubConsumer.Send(msg)

	// Wait for processing
	time.Sleep(50 * time.Millisecond)

	cancel()
	<-done

	// Verify message was terminated (invalid payload)
	assert.True(t, msg.IsTermed())
	// Worker should NOT be called
	mockWorker.AssertNotCalled(t, "ProcessTask")
}

func TestConsumer_GracefulShutdown_MessageNakOnClose(t *testing.T) {
	mockPubsubConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	nc := NewTaskConsumer(mockPubsubConsumer, mockWorker, 1, nil)
	consumer := nc.(*natsConsumer)

	// Initialize worker channels
	consumer.workerChans = make([]chan pubsub.Message, 1)
	consumer.workerChans[0] = make(chan pubsub.Message, 10)

	// Set closing state
	consumer.closing.Store(true)

	// Create a mock message
	task := &types.DeliveryTask{TriggerID: "t1"}
	taskBytes, _ := json.Marshal(task)
	msg := pubsubtesting.NewMockMessage("test.subject", taskBytes)

	// Call dispatch directly
	consumer.dispatch(msg)

	// Verify NAK was called
	assert.True(t, msg.IsNaked())
	// Worker should NOT be called
	mockWorker.AssertNotCalled(t, "ProcessTask")
}

func TestConsumer_WaitForDrain(t *testing.T) {
	mockPubsubConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	nc := NewTaskConsumer(mockPubsubConsumer, mockWorker, 1, nil)
	consumer := nc.(*natsConsumer)

	// Test immediate drain (no in-flight messages)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	start := time.Now()
	consumer.waitForDrain(ctx)
	elapsed := time.Since(start)

	// Should return quickly (< 200ms)
	assert.Less(t, elapsed, 200*time.Millisecond)
}

func TestConsumer_WaitForDrain_Timeout(t *testing.T) {
	mockPubsubConsumer := pubsubtesting.NewMockConsumer()
	mockWorker := new(MockWorker)

	nc := NewTaskConsumer(mockPubsubConsumer, mockWorker, 1, nil)
	consumer := nc.(*natsConsumer)

	// Simulate in-flight messages
	consumer.inFlightCount.Store(5)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	consumer.waitForDrain(ctx)
	elapsed := time.Since(start)

	// Should timeout after ~100ms
	assert.GreaterOrEqual(t, elapsed, 95*time.Millisecond)
	assert.Less(t, elapsed, 300*time.Millisecond)
}
