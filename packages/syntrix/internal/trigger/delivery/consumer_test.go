package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/codetreker/syntrix/internal/core/pubsub/memory"
	pubsubtesting "github.com/codetreker/syntrix/internal/core/pubsub/testing"
	"github.com/codetreker/syntrix/internal/trigger/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- Mocks ---

type MockWorker struct {
	mock.Mock
}

func (m *MockWorker) ProcessTask(ctx context.Context, task *types.DeliveryTask) error {
	args := m.Called(ctx, task)
	return args.Error(0)
}

type stateConsumer struct {
	messages chan pubsub.Message
	ready    atomic.Bool
	mu       sync.Mutex
	err      error
	closed   sync.Once
	holdOpen bool
}

func (c *stateConsumer) Subscribe(ctx context.Context) (<-chan pubsub.Message, error) {
	c.ready.Store(true)
	if !c.holdOpen {
		context.AfterFunc(ctx, c.close)
	}
	return c.messages, nil
}

func (c *stateConsumer) close() { c.closed.Do(func() { close(c.messages) }) }

func (c *stateConsumer) Ready() bool { return c.ready.Load() }
func (c *stateConsumer) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

type deliveryWorkerFunc func(context.Context, *types.DeliveryTask) error

func (f deliveryWorkerFunc) ProcessTask(ctx context.Context, task *types.DeliveryTask) error {
	return f(ctx, task)
}

func TestConsumer_ReadinessAndTerminalClosure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &stateConsumer{messages: make(chan pubsub.Message)}
	c := NewTaskConsumer(source, deliveryWorkerFunc(func(context.Context, *types.DeliveryTask) error { return nil }), 2, nil,
		WithDrainTimeout(time.Millisecond)).(*natsConsumer)
	s := &service{consumer: c}
	require.Error(t, s.Readiness(ctx))
	require.NoError(t, s.TerminalError())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	require.Eventually(t, func() bool { return s.Readiness(ctx) == nil }, time.Second, time.Millisecond)
	source.ready.Store(false)
	require.Error(t, s.Readiness(ctx))
	source.ready.Store(true)
	require.NoError(t, s.Readiness(ctx))
	cancelled, stop := context.WithCancel(ctx)
	stop()
	require.ErrorIs(t, s.Readiness(cancelled), context.Canceled)
	require.ErrorContains(t, s.Start(ctx), "already running")
	cause := errors.New("consumer deleted")
	source.mu.Lock()
	source.err = cause
	source.mu.Unlock()
	require.ErrorIs(t, s.Readiness(ctx), cause)
	require.ErrorIs(t, s.TerminalError(), cause)
	source.close()
	require.ErrorIs(t, <-done, cause)
	require.ErrorIs(t, s.TerminalError(), cause)
	require.ErrorIs(t, s.Readiness(ctx), cause)
}

func TestService_UnobservablePoolIsUnready(t *testing.T) {
	s := &service{consumer: new(MockTaskConsumer)}
	require.ErrorContains(t, s.Readiness(context.Background()), "state is unavailable")
	require.NoError(t, s.TerminalError())
	require.ErrorContains(t, s.Shutdown(context.Background()), "settlement is unavailable")
	c := NewTaskConsumer(pubsubtesting.NewMockConsumer(), new(MockWorker), 1, nil).(*natsConsumer)
	require.NoError(t, c.TerminalError())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	require.Eventually(t, func() bool { return c.activeWorkers.Load() == 1 }, time.Second, time.Millisecond)
	require.ErrorContains(t, c.Readiness(ctx), "consumer is not active")
	cancel()
	require.NoError(t, <-done)
}

func TestConsumer_UnexpectedClosureWithoutCause(t *testing.T) {
	source := &stateConsumer{messages: make(chan pubsub.Message)}
	source.close()
	c := NewTaskConsumer(source, deliveryWorkerFunc(func(context.Context, *types.DeliveryTask) error { return nil }), 1, nil,
		WithDrainTimeout(time.Millisecond)).(*natsConsumer)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.ErrorContains(t, c.Start(ctx), "stopped unexpectedly")
	require.Error(t, c.TerminalError())
}

func TestConsumer_CancelReleasesBlockedDispatchAndDrains(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &stateConsumer{messages: make(chan pubsub.Message, 5)}
	processing := make(chan struct{})
	c := NewTaskConsumer(source, deliveryWorkerFunc(func(ctx context.Context, _ *types.DeliveryTask) error {
		close(processing)
		<-ctx.Done()
		return ctx.Err()
	}), 1, nil, WithChannelBufferSize(1), WithDrainTimeout(100*time.Millisecond)).(*natsConsumer)
	data, err := json.Marshal(types.DeliveryTask{TriggerID: "cancel", Collection: "same", DocumentID: "same"})
	require.NoError(t, err)
	messages := make([]*pubsubtesting.MockMessage, 5)
	for i := range messages {
		messages[i] = pubsubtesting.NewMockMessage("subject", data)
		source.messages <- messages[i]
	}
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-processing:
	case <-ctx.Done():
		t.Fatal("worker never received a message")
	}
	require.Eventually(t, func() bool { return c.inFlightCount.Load() == 1 && len(c.workerChans[0]) == 1 }, time.Second, time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	require.Zero(t, c.activeWorkers.Load())
	require.NoError(t, c.TerminalError())
	require.Error(t, c.Readiness(context.Background()))
	for _, message := range messages {
		require.True(t, message.IsNaked(), "cancelled messages remain available for redelivery")
	}
}

func TestConsumer_UnsettledSourceShutdownIsTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &stateConsumer{messages: make(chan pubsub.Message), holdOpen: true}
	c := NewTaskConsumer(source, deliveryWorkerFunc(func(context.Context, *types.DeliveryTask) error { return nil }), 1, nil,
		WithDrainTimeout(time.Millisecond)).(*natsConsumer)
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	require.Eventually(t, func() bool { return c.Readiness(ctx) == nil }, time.Second, time.Millisecond)
	cancel()
	require.ErrorContains(t, <-done, "consumer shutdown timed out")
	require.Error(t, c.TerminalError())
	bounded, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	require.ErrorIs(t, c.Shutdown(bounded), context.DeadlineExceeded)
	source.close()
	require.NoError(t, c.Shutdown(context.Background()))
}

func TestConsumer_ShutdownTimeoutIsTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &stateConsumer{messages: make(chan pubsub.Message, 1)}
	processing, release := make(chan struct{}), make(chan struct{})
	c := NewTaskConsumer(source, deliveryWorkerFunc(func(context.Context, *types.DeliveryTask) error {
		close(processing)
		<-release
		return nil
	}), 1, nil, WithDrainTimeout(time.Millisecond), WithShutdownTimeout(time.Millisecond)).(*natsConsumer)
	data, err := json.Marshal(types.DeliveryTask{TriggerID: "timeout"})
	require.NoError(t, err)
	source.messages <- pubsubtesting.NewMockMessage("subject", data)
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-processing:
	case <-ctx.Done():
		t.Fatal("worker never received a message")
	}
	cancel()
	require.ErrorContains(t, <-done, "shutdown timed out")
	require.Error(t, c.TerminalError())
	bounded, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	require.ErrorIs(t, c.Shutdown(bounded), context.DeadlineExceeded)
	close(release)
	require.NoError(t, c.Shutdown(context.Background()))
}

func TestConsumer_CancellationClearsServingBeforeWorkerReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &stateConsumer{messages: make(chan pubsub.Message, 1)}
	processing, release := make(chan struct{}), make(chan struct{})
	c := NewTaskConsumer(source, deliveryWorkerFunc(func(ctx context.Context, _ *types.DeliveryTask) error {
		close(processing)
		<-release
		return ctx.Err()
	}), 1, nil, WithDrainTimeout(100*time.Millisecond), WithShutdownTimeout(5*time.Second)).(*natsConsumer)
	data, err := json.Marshal(types.DeliveryTask{TriggerID: "blocked"})
	require.NoError(t, err)
	source.messages <- pubsubtesting.NewMockMessage("subject", data)
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	select {
	case <-processing:
	case <-ctx.Done():
		t.Fatal("worker did not start")
	}
	require.NoError(t, c.Readiness(ctx))
	cancel()
	require.ErrorIs(t, c.Readiness(context.Background()), context.Canceled)
	bounded, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	require.ErrorIs(t, c.Shutdown(bounded), context.DeadlineExceeded)
	close(release)
	require.NoError(t, <-done)
	require.Zero(t, c.activeWorkers.Load())
	require.NoError(t, c.Shutdown(context.Background()))
}

func TestService_MemoryConsumerIdleAdmissionAndSettlement(t *testing.T) {
	for _, terminate := range []bool{false, true} {
		t.Run(fmt.Sprint(terminate), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			engine := memory.New()
			defer engine.Close()
			consumer, err := engine.NewConsumer(ctx, pubsub.ConsumerOptions{FilterSubject: "managed.>", ChannelBufSize: 10})
			require.NoError(t, err)
			svc, err := NewService(Dependencies{Consumer: consumer}, Config{NumWorkers: 2, DrainTimeout: time.Second, ShutdownTimeout: time.Second})
			require.NoError(t, err)
			s := svc.(*service)
			done := make(chan error, 1)
			go func() { done <- s.Start(ctx) }()
			require.Eventually(t, func() bool { return s.Readiness(ctx) == nil }, time.Second, time.Millisecond,
				"an idle worker must prove real subscription admission without a task write")
			if terminate {
				require.NoError(t, engine.Close())
				require.ErrorIs(t, <-done, memory.ErrEngineClosed)
				require.ErrorIs(t, s.TerminalError(), memory.ErrEngineClosed)
			} else {
				cancel()
				require.NoError(t, <-done)
				require.NoError(t, s.TerminalError())
			}
			require.False(t, consumer.(pubsub.ConsumerState).Ready())
			require.Error(t, s.Readiness(context.Background()))
			require.NoError(t, s.Shutdown(context.Background()))
		})
	}
}

func TestConsumer_ShutdownOwnsCancellationAndSerializesSettlement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &stateConsumer{messages: make(chan pubsub.Message), holdOpen: true}
	c := NewTaskConsumer(source, deliveryWorkerFunc(func(context.Context, *types.DeliveryTask) error { return nil }), 1, nil,
		WithDrainTimeout(time.Millisecond)).(*natsConsumer)
	require.NoError(t, c.Shutdown(ctx))
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	require.Eventually(t, func() bool { return c.Readiness(ctx) == nil }, time.Second, time.Millisecond)
	firstShutdown := make(chan error, 1)
	go func() { firstShutdown <- c.Shutdown(ctx) }()
	require.ErrorContains(t, <-done, "consumer shutdown timed out")
	message := pubsubtesting.NewMockMessage("subject", []byte("pending"))
	select {
	case source.messages <- message:
	case <-ctx.Done():
		t.Fatal("settlement did not resume the source drain")
	}
	bounded, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	require.ErrorIs(t, c.Shutdown(bounded), context.DeadlineExceeded)
	source.close()
	require.NoError(t, <-firstShutdown)
	require.True(t, message.IsNaked())
	require.NoError(t, c.Shutdown(ctx))
	require.ErrorContains(t, c.Start(ctx), "already running or stopped")
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
