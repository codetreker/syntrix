package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	pubsubtesting "github.com/codetreker/syntrix/internal/core/pubsub/testing"
	"github.com/codetreker/syntrix/internal/trigger/delivery/worker"
	"github.com/codetreker/syntrix/internal/trigger/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestConsumer_Worker_HTTPResponses(t *testing.T) {
	tests := []struct {
		name       string
		statuses   []int
		headers    []string
		maxBackoff time.Duration
		nakDelays  []time.Duration
		wantAck    bool
		wantTerm   bool
		fatalHint  bool
	}{
		{
			name:       "rate_limit_then_success",
			maxBackoff: 3 * time.Second,
			statuses:   []int{http.StatusTooManyRequests, http.StatusNoContent},
			nakDelays:  []time.Duration{2 * time.Second, 0},
			wantAck:    true,
		},
		{
			name:       "rate_limit_exhaustion",
			maxBackoff: 3 * time.Second,
			statuses:   []int{http.StatusTooManyRequests, http.StatusTooManyRequests, http.StatusTooManyRequests},
			nakDelays:  []time.Duration{2 * time.Second, 3 * time.Second, 0},
			wantTerm:   true,
		},
		{
			name:       "bad_request",
			headers:    []string{"60"},
			maxBackoff: 3 * time.Second,
			statuses:   []int{http.StatusBadRequest},
			nakDelays:  []time.Duration{0},
			wantTerm:   true,
		},
		{
			name:       "server_error",
			headers:    []string{"60"},
			maxBackoff: 3 * time.Second,
			statuses:   []int{http.StatusInternalServerError},
			nakDelays:  []time.Duration{2 * time.Second},
		},
		{
			name:       "hint_exceeds_rule_cap",
			statuses:   []int{http.StatusTooManyRequests, http.StatusNoContent},
			headers:    []string{"60", "60"},
			maxBackoff: 5 * time.Second,
			nakDelays:  []time.Duration{time.Minute, 0},
			wantAck:    true,
		},
		{
			name:       "short_hint_preserves_rule_backoff",
			statuses:   []int{http.StatusTooManyRequests},
			headers:    []string{"1"},
			maxBackoff: 5 * time.Second,
			nakDelays:  []time.Duration{2 * time.Second},
		},
		{
			name:       "invalid_hint_preserves_rule_backoff",
			statuses:   []int{http.StatusTooManyRequests},
			headers:    []string{"later"},
			maxBackoff: 5 * time.Second,
			nakDelays:  []time.Duration{2 * time.Second},
		},
		{
			name:      "hint_without_rule_cap",
			statuses:  []int{http.StatusTooManyRequests},
			headers:   []string{"60"},
			nakDelays: []time.Duration{time.Minute},
		},
		{
			name:       "hint_preserves_attempt_budget",
			statuses:   []int{http.StatusTooManyRequests, http.StatusTooManyRequests, http.StatusTooManyRequests},
			headers:    []string{"60", "60", "60"},
			maxBackoff: 5 * time.Second,
			nakDelays:  []time.Duration{time.Minute, time.Minute, 0},
			wantTerm:   true,
		},
		{
			name:      "seconds_overflow_terminates",
			statuses:  []int{http.StatusTooManyRequests},
			headers:   []string{"9223372037"},
			nakDelays: []time.Duration{0},
			wantTerm:  true,
			fatalHint: true,
		},
		{
			name:      "date_overflow_terminates",
			statuses:  []int{http.StatusTooManyRequests},
			headers:   []string{"Fri, 31 Dec 9999 23:59:59 GMT"},
			nakDelays: []time.Duration{0},
			wantTerm:  true,
			fatalHint: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := int(requests.Add(1))
				if attempt > len(tt.statuses) {
					t.Errorf("unexpected HTTP request %d", attempt)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if len(tt.headers) > 0 {
					w.Header().Set("Retry-After", tt.headers[attempt-1])
				}
				w.WriteHeader(tt.statuses[attempt-1])
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			workerMetrics := new(MockMetrics)
			workerMetrics.Test(t)
			for _, status := range tt.statuses {
				switch status {
				case http.StatusNoContent:
					workerMetrics.On("IncDeliverySuccess", "database1", "users").Once()
					workerMetrics.On("ObserveDeliveryLatency", "database1", "users", mock.Anything).Once()
				case http.StatusBadRequest:
					workerMetrics.On("IncDeliveryFailure", "database1", "users", status, true).Once()
				default:
					workerMetrics.On("IncDeliveryFailure", "database1", "users", status, tt.fatalHint).Once()
				}
			}
			c := &natsConsumer{
				worker:     worker.NewDeliveryWorker(nil, nil, worker.HTTPClientOptions{}, workerMetrics),
				numWorkers: 1,
				metrics:    &types.NoopMetrics{},
			}
			data, err := json.Marshal(&types.DeliveryTask{
				TriggerID:  "http-response-trigger",
				Database:   "database1",
				Collection: "users",
				URL:        server.URL,
				RetryPolicy: types.RetryPolicy{
					MaxAttempts:    3,
					InitialBackoff: types.Duration(2 * time.Second),
					MaxBackoff:     types.Duration(tt.maxBackoff),
				},
			})
			require.NoError(t, err)

			for i := range tt.statuses {
				// The mock records the requested delay; each new message simulates the next broker delivery.
				msg := pubsubtesting.NewMockMessage("test.subject", data)
				msg.SetMetadata(pubsub.MessageMetadata{NumDelivered: uint64(i + 1)})
				c.workerChans = []chan pubsub.Message{make(chan pubsub.Message, 1)}
				c.workerChans[0] <- msg
				close(c.workerChans[0])
				c.wg.Add(1)
				c.workerLoop(ctx, 0)

				last := i == len(tt.statuses)-1
				require.Equal(t, tt.nakDelays[i] > 0, msg.IsNaked(), "delivery %d", i+1)
				require.Equal(t, tt.nakDelays[i], msg.NakDelay(), "delivery %d", i+1)
				require.Equal(t, last && tt.wantAck, msg.IsAcked(), "delivery %d", i+1)
				require.Equal(t, last && tt.wantTerm, msg.IsTermed(), "delivery %d", i+1)
				require.Equal(t, int32(i+1), requests.Load())
			}
			workerMetrics.AssertExpectations(t)
		})
	}
}

func TestConsumer_Worker_RetrySchedulingFailure(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	mockWorker := new(MockWorker)
	mockWorker.On("ProcessTask", mock.Anything, mock.Anything).Return(&types.RetryAfterError{
		Err: errors.New("webhook failed with status: 429"), Delay: time.Minute,
	}).Once()
	c := &natsConsumer{
		worker: mockWorker, metrics: &types.NoopMetrics{},
		workerChans: []chan pubsub.Message{make(chan pubsub.Message, 1)},
	}
	data, err := json.Marshal(&types.DeliveryTask{TriggerID: "retry-scheduling-failure"})
	require.NoError(t, err)
	msg := pubsubtesting.NewMockMessage("test.subject", data)
	msg.SetErrors(nil, errors.New("queue unavailable"), nil, nil)
	c.workerChans[0] <- msg
	close(c.workerChans[0])
	c.wg.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.workerLoop(ctx, 0)

	assert.False(t, msg.IsAcked())
	assert.False(t, msg.IsNaked())
	assert.False(t, msg.IsTermed())
	assert.Contains(t, logs.String(), `"level":"ERROR","msg":"Failed to schedule trigger retry"`)
	assert.Contains(t, logs.String(), `"trigger_id":"retry-scheduling-failure"`)
	assert.Contains(t, logs.String(), `"error":"queue unavailable"`)
	mockWorker.AssertExpectations(t)
}

// TestConsumer_Dispatch_InvalidPayload verifies that invalid payloads are Terminated.
func TestConsumer_Dispatch_InvalidPayload(t *testing.T) {
	// Setup
	c := &natsConsumer{
		numWorkers:  1,
		workerChans: []chan pubsub.Message{make(chan pubsub.Message, 1)},
		metrics:     &types.NoopMetrics{},
	}

	msg := pubsubtesting.NewMockMessage("test.subject", []byte("invalid-json"))

	// Execute
	c.dispatch(msg)

	// Verify - invalid payload should be terminated
	assert.True(t, msg.IsTermed())
}

// TestConsumer_Worker_RetryLogic verifies the retry logic in workerLoop.
func TestConsumer_Worker_RetryLogic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		processErr     error
		metadataErr    error
		numDelivered   uint64
		maxAttempts    int
		initialBackoff time.Duration
		maxBackoff     time.Duration
		payload        interface{} // string (invalid) or DeliveryTask
		expectTerm     bool
		expectNak      bool
		expectNakDelay time.Duration
		expectAck      bool
	}{
		{
			name:       "Success",
			processErr: nil,
			payload: &types.DeliveryTask{
				TriggerID: "t1",
			},
			expectAck: true,
		},
		{
			name:         "ProcessError_Retry",
			processErr:   errors.New("fail"),
			numDelivered: 1,
			maxAttempts:  3,
			payload: &types.DeliveryTask{
				TriggerID: "t1",
				RetryPolicy: types.RetryPolicy{
					MaxAttempts:    3,
					InitialBackoff: types.Duration(1 * time.Second),
				},
			},
			expectNak:      true,
			expectNakDelay: 1 * time.Second,
		},
		{
			name:         "ProcessError_MaxAttemptsReached",
			processErr:   errors.New("fail"),
			numDelivered: 3,
			maxAttempts:  3,
			payload: &types.DeliveryTask{
				TriggerID: "t1",
				RetryPolicy: types.RetryPolicy{
					MaxAttempts: 3,
				},
			},
			expectTerm: true,
		},
		{
			name:           "Timeout_Retry",
			processErr:     fmt.Errorf("request failed: %w", context.DeadlineExceeded),
			numDelivered:   1,
			payload:        &types.DeliveryTask{TriggerID: "t1"},
			expectNak:      true,
			expectNakDelay: time.Second,
		},
		{
			name:         "Timeout_MaxAttemptsReached",
			processErr:   fmt.Errorf("request failed: %w", context.DeadlineExceeded),
			numDelivered: 3,
			payload:      &types.DeliveryTask{TriggerID: "t1"},
			expectTerm:   true,
		},
		{
			name: "WrappedRetryAfter",
			processErr: fmt.Errorf("delivery failed: %w", &types.RetryAfterError{
				Err:   errors.New("webhook failed with status: 429"),
				Delay: time.Minute,
			}),
			numDelivered: 1,
			payload: &types.DeliveryTask{
				TriggerID: "t1",
				RetryPolicy: types.RetryPolicy{
					MaxAttempts:    3,
					InitialBackoff: types.Duration(time.Second),
					MaxBackoff:     types.Duration(5 * time.Second),
				},
			},
			expectNak:      true,
			expectNakDelay: time.Minute,
		},
		{
			name:         "ProcessError_Fatal",
			processErr:   &types.FatalError{Err: errors.New("fatal")},
			numDelivered: 1,
			maxAttempts:  3,
			payload: &types.DeliveryTask{
				TriggerID: "t1",
			},
			expectTerm: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockWorker := new(MockWorker)
			c := &natsConsumer{
				worker:      mockWorker,
				numWorkers:  1,
				workerChans: []chan pubsub.Message{make(chan pubsub.Message, 1)},
				metrics:     &types.NoopMetrics{},
			}
			c.wg.Add(1)

			var msg *pubsubtesting.MockMessage

			// Setup expectations
			if tt.payload != nil {
				if task, ok := tt.payload.(*types.DeliveryTask); ok {
					data, _ := json.Marshal(task)
					msg = pubsubtesting.NewMockMessage("test.subject", data)
					msg.SetMetadata(pubsub.MessageMetadata{NumDelivered: tt.numDelivered})

					if tt.processErr != nil {
						mockWorker.On("ProcessTask", mock.Anything, mock.Anything).Return(tt.processErr)
					} else {
						mockWorker.On("ProcessTask", mock.Anything, mock.Anything).Return(nil)
					}
				}
			}

			// Inject message
			c.workerChans[0] <- msg
			close(c.workerChans[0])

			// Run worker loop
			c.workerLoop(context.Background(), 0)

			// Verify
			if tt.expectAck {
				assert.True(t, msg.IsAcked())
			}
			if tt.expectTerm {
				assert.True(t, msg.IsTermed())
			}
			if tt.expectNak {
				assert.True(t, msg.IsNaked())
				assert.Equal(t, tt.expectNakDelay, msg.NakDelay())
			}
			mockWorker.AssertExpectations(t)
		})
	}
}

// TestConsumer_Worker_MetadataError verifies handling of metadata retrieval errors.
func TestConsumer_Worker_MetadataError(t *testing.T) {
	mockWorker := new(MockWorker)
	c := &natsConsumer{
		worker:      mockWorker,
		numWorkers:  1,
		workerChans: []chan pubsub.Message{make(chan pubsub.Message, 1)},
		metrics:     &types.NoopMetrics{},
	}
	c.wg.Add(1)

	task := &types.DeliveryTask{TriggerID: "t1"}
	data, _ := json.Marshal(task)
	msg := pubsubtesting.NewMockMessage("test.subject", data)
	msg.SetErrors(nil, nil, nil, errors.New("metadata error"))

	mockWorker.On("ProcessTask", mock.Anything, mock.Anything).Return(errors.New("process failed"))

	c.workerChans[0] <- msg
	close(c.workerChans[0])

	c.workerLoop(context.Background(), 0)

	// On metadata error, should Nak without delay
	assert.True(t, msg.IsNaked())
	assert.Equal(t, time.Duration(0), msg.NakDelay())
	mockWorker.AssertExpectations(t)
}

// TestConsumer_Worker_MaxBackoffLimit verifies that backoff is capped at maxBackoff.
func TestConsumer_Worker_MaxBackoffLimit(t *testing.T) {
	mockWorker := new(MockWorker)
	c := &natsConsumer{
		worker:      mockWorker,
		numWorkers:  1,
		workerChans: []chan pubsub.Message{make(chan pubsub.Message, 1)},
		metrics:     &types.NoopMetrics{},
	}
	c.wg.Add(1)

	task := &types.DeliveryTask{
		TriggerID: "t1",
		RetryPolicy: types.RetryPolicy{
			MaxAttempts:    10,
			InitialBackoff: types.Duration(1 * time.Second),
			MaxBackoff:     types.Duration(5 * time.Second), // Cap at 5s
		},
	}
	data, _ := json.Marshal(task)
	msg := pubsubtesting.NewMockMessage("test.subject", data)
	// NumDelivered=5 means attempt 5, exponential backoff would be 16s, but capped at 5s
	msg.SetMetadata(pubsub.MessageMetadata{NumDelivered: 5})

	mockWorker.On("ProcessTask", mock.Anything, mock.Anything).Return(errors.New("process failed"))

	c.workerChans[0] <- msg
	close(c.workerChans[0])

	c.workerLoop(context.Background(), 0)

	assert.True(t, msg.IsNaked())
	assert.Equal(t, 5*time.Second, msg.NakDelay())
	mockWorker.AssertExpectations(t)
}

// TestConsumer_Worker_DefaultRetryValues verifies default retry values are used when not specified.
func TestConsumer_Worker_DefaultRetryValues(t *testing.T) {
	mockWorker := new(MockWorker)
	c := &natsConsumer{
		worker:      mockWorker,
		numWorkers:  1,
		workerChans: []chan pubsub.Message{make(chan pubsub.Message, 1)},
		metrics:     &types.NoopMetrics{},
	}
	c.wg.Add(1)

	// Task with no retry policy - should use defaults (maxAttempts=3, initialBackoff=1s)
	task := &types.DeliveryTask{
		TriggerID: "t1",
	}
	data, _ := json.Marshal(task)
	msg := pubsubtesting.NewMockMessage("test.subject", data)
	msg.SetMetadata(pubsub.MessageMetadata{NumDelivered: 1})

	mockWorker.On("ProcessTask", mock.Anything, mock.Anything).Return(errors.New("process failed"))

	c.workerChans[0] <- msg
	close(c.workerChans[0])

	c.workerLoop(context.Background(), 0)

	assert.True(t, msg.IsNaked())
	assert.Equal(t, 1*time.Second, msg.NakDelay()) // Default initial backoff
	mockWorker.AssertExpectations(t)
}

func TestConsumer_ProcessMsg_WithTimeout(t *testing.T) {
	for _, tt := range []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{name: "custom", timeout: 2 * time.Second, want: 2 * time.Second},
		{name: "default", want: 30 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			mockWorker := new(MockWorker)
			c := &natsConsumer{worker: mockWorker, metrics: &types.NoopMetrics{}}
			data, err := json.Marshal(&types.DeliveryTask{TriggerID: "t1", Timeout: types.Duration(tt.timeout)})
			require.NoError(t, err)
			msg := pubsubtesting.NewMockMessage("test.subject", data)
			started := time.Now()
			var taskCtx context.Context
			mockWorker.On("ProcessTask", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				taskCtx = args.Get(0).(context.Context)
				deadline, ok := taskCtx.Deadline()
				require.True(t, ok)
				assert.False(t, deadline.Before(started.Add(tt.want)))
				assert.False(t, deadline.After(time.Now().Add(tt.want)))
			}).Return(nil).Once()

			require.NoError(t, c.processMsg(ctx, msg))
			require.NotNil(t, taskCtx)
			assert.ErrorIs(t, taskCtx.Err(), context.Canceled)
			mockWorker.AssertExpectations(t)
		})
	}
}

// TestConsumer_ProcessMsg_InvalidPayload verifies error handling for invalid payload.
func TestConsumer_ProcessMsg_InvalidPayload(t *testing.T) {
	mockWorker := new(MockWorker)
	c := &natsConsumer{
		worker:  mockWorker,
		metrics: &types.NoopMetrics{},
	}

	msg := pubsubtesting.NewMockMessage("test.subject", []byte("invalid-json"))

	err := c.processMsg(context.Background(), msg)
	assert.ErrorContains(t, err, "invalid payload")
}

// TestConsumer_ProcessMsg_WorkerError verifies metrics are recorded on worker error.
func TestConsumer_ProcessMsg_WorkerError(t *testing.T) {
	mockWorker := new(MockWorker)
	mockMetrics := new(MockMetrics)
	c := &natsConsumer{
		worker:  mockWorker,
		metrics: mockMetrics,
	}

	task := &types.DeliveryTask{
		TriggerID:  "t1",
		Database:   "database1",
		Collection: "col1",
	}
	data, _ := json.Marshal(task)
	msg := pubsubtesting.NewMockMessage("test.subject", data)

	mockWorker.On("ProcessTask", mock.Anything, mock.Anything).Return(errors.New("worker failed"))
	mockMetrics.On("IncConsumeFailure", "database1", "col1", "worker failed").Return()
	mockMetrics.On("ObserveConsumeLatency", "database1", "col1", mock.Anything).Return()

	err := c.processMsg(context.Background(), msg)
	assert.Error(t, err)

	mockWorker.AssertExpectations(t)
	mockMetrics.AssertExpectations(t)
}

// MockMetrics is a mock implementation of types.Metrics for testing.
type MockMetrics struct {
	mock.Mock
}

func (m *MockMetrics) IncPublishSuccess(database, collection string, hashed bool) {
	m.Called(database, collection, hashed)
}

func (m *MockMetrics) IncPublishFailure(database, collection, reason string) {
	m.Called(database, collection, reason)
}

func (m *MockMetrics) IncConsumeSuccess(database, collection string, hashed bool) {
	m.Called(database, collection, hashed)
}

func (m *MockMetrics) IncConsumeFailure(database, collection, reason string) {
	m.Called(database, collection, reason)
}

func (m *MockMetrics) ObservePublishLatency(database, collection string, d time.Duration) {
	m.Called(database, collection, d)
}

func (m *MockMetrics) ObserveConsumeLatency(database, collection string, d time.Duration) {
	m.Called(database, collection, d)
}

func (m *MockMetrics) IncHashCollision(database, collection string) {
	m.Called(database, collection)
}

func (m *MockMetrics) IncDeliverySuccess(database, collection string) {
	m.Called(database, collection)
}

func (m *MockMetrics) IncDeliveryFailure(database, collection string, status int, fatal bool) {
	m.Called(database, collection, status, fatal)
}

func (m *MockMetrics) IncDeliveryRetry(database, collection string) {
	m.Called(database, collection)
}

func (m *MockMetrics) ObserveDeliveryLatency(database, collection string, d time.Duration) {
	m.Called(database, collection, d)
}
