package client

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"google.golang.org/grpc"
)

// mockSubscribeClient implements pullerv1.PullerService_SubscribeClient
type mockSubscribeClient struct {
	grpc.ClientStream
	events    []*pullerv1.PullerEvent
	index     int
	mu        sync.Mutex
	failAfter int   // Return error after this many events (-1 = never)
	failError error // Error to return when failing
}

func (m *mockSubscribeClient) Recv() (*pullerv1.PullerEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if we should fail
	if m.failAfter >= 0 && m.index >= m.failAfter {
		if m.failError != nil {
			return nil, m.failError
		}
		return nil, io.EOF
	}

	if m.index >= len(m.events) {
		return nil, io.EOF
	}
	evt := m.events[m.index]
	m.index++
	return evt, nil
}

// mockPullerServiceClient implements pullerv1.PullerServiceClient
type mockPullerServiceClient struct {
	pullerv1.PullerServiceClient
	subscribeFunc func(ctx context.Context, in *pullerv1.SubscribeRequest, opts ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error)
}

func (m *mockPullerServiceClient) Subscribe(ctx context.Context, in *pullerv1.SubscribeRequest, opts ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
	return m.subscribeFunc(ctx, in, opts...)
}

func TestNew(t *testing.T) {
	t.Parallel()
	c, err := New("localhost:50051", nil)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.NotNil(t, c.logger)

	_ = c.Close()
}

func TestNewWithConfig(t *testing.T) {
	t.Parallel()
	cfg := ClientConfig{
		InitialBackoff:    500 * time.Millisecond,
		MaxBackoff:        10 * time.Second,
		BackoffMultiplier: 1.5,
		MaxRetries:        5,
	}

	c, err := NewWithConfig("localhost:50051", nil, cfg)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, 500*time.Millisecond, c.cfg.InitialBackoff)
	assert.Equal(t, 5, c.cfg.MaxRetries)

	_ = c.Close()
}

func TestNewWithConfig_Defaults(t *testing.T) {
	t.Parallel()
	cfg := ClientConfig{} // All zeros

	c, err := NewWithConfig("localhost:50051", nil, cfg)
	require.NoError(t, err)

	assert.Equal(t, 1*time.Second, c.cfg.InitialBackoff)
	assert.Equal(t, 30*time.Second, c.cfg.MaxBackoff)
	assert.Equal(t, 2.0, c.cfg.BackoffMultiplier)

	_ = c.Close()
}

func TestDefaultClientConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultClientConfig()

	assert.Equal(t, 1*time.Second, cfg.InitialBackoff)
	assert.Equal(t, 30*time.Second, cfg.MaxBackoff)
	assert.Equal(t, 2.0, cfg.BackoffMultiplier)
	assert.Equal(t, 0, cfg.MaxRetries)
}

func TestClient_Close(t *testing.T) {
	t.Parallel()
	// Test Close with nil conn
	c := &Client{}
	assert.NoError(t, c.Close())
}

func TestClient_Close_Real(t *testing.T) {
	t.Parallel()
	c, err := New("localhost:50051", slog.Default())
	require.NoError(t, err)

	err = c.Close()
	assert.NoError(t, err)
}

func TestClient_Subscribe_BasicFlow(t *testing.T) {
	t.Parallel()

	events := []*pullerv1.PullerEvent{
		{
			ChangeEvent: &pullerv1.ChangeEvent{
				EventId: "evt-1",
				OpType:  "insert",
			},
			Progress: "p1",
		},
		{
			ChangeEvent: &pullerv1.ChangeEvent{
				EventId: "evt-2",
				OpType:  "update",
			},
			Progress: "p2",
		},
	}

	mockClient := &mockPullerServiceClient{
		subscribeFunc: func(ctx context.Context, in *pullerv1.SubscribeRequest, opts ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
			return &mockSubscribeClient{events: events, failAfter: -1}, nil
		},
	}

	c := &Client{
		address: "localhost:50051",
		client:  mockClient,
		logger:  slog.Default(),
		cfg:     DefaultClientConfig(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := c.Subscribe(ctx, "consumer-1", "")

	// Collect events
	var received []*pullerv1.PullerEvent
	for evt := range ch {
		received = append(received, &pullerv1.PullerEvent{
			ChangeEvent: &pullerv1.ChangeEvent{
				EventId: evt.Change.EventID,
			},
			Progress: evt.Progress,
		})
		if len(received) >= 2 {
			cancel()
		}
	}

	require.Len(t, received, 2)
	assert.Equal(t, "evt-1", received[0].ChangeEvent.EventId)
	assert.Equal(t, "evt-2", received[1].ChangeEvent.EventId)
}

func TestClient_Subscribe_HeartbeatFiltering(t *testing.T) {
	t.Parallel()

	events := []*pullerv1.PullerEvent{
		{
			ChangeEvent: &pullerv1.ChangeEvent{EventId: "evt-1"},
			Progress:    "p1",
		},
		{
			// Heartbeat - nil ChangeEvent
			ChangeEvent: nil,
			Progress:    "p2",
		},
		{
			ChangeEvent: &pullerv1.ChangeEvent{EventId: "evt-2"},
			Progress:    "p3",
		},
	}

	mockClient := &mockPullerServiceClient{
		subscribeFunc: func(ctx context.Context, in *pullerv1.SubscribeRequest, opts ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
			return &mockSubscribeClient{events: events, failAfter: -1}, nil
		},
	}

	c := &Client{
		address: "localhost:50051",
		client:  mockClient,
		logger:  slog.Default(),
		cfg:     DefaultClientConfig(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := c.Subscribe(ctx, "consumer-1", "")

	// Collect events - heartbeats should be filtered
	var received int
	for range ch {
		received++
		if received >= 2 {
			cancel()
		}
	}

	// Should receive only 2 events (heartbeat filtered out)
	assert.Equal(t, 2, received)
}

func TestClient_Subscribe_AutoReconnect(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32

	mockClient := &mockPullerServiceClient{
		subscribeFunc: func(ctx context.Context, in *pullerv1.SubscribeRequest, opts ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
			count := callCount.Add(1)

			if count == 1 {
				// First call: return one event then fail
				return &mockSubscribeClient{
					events: []*pullerv1.PullerEvent{
						{
							ChangeEvent: &pullerv1.ChangeEvent{EventId: "evt-1"},
							Progress:    "progress-1",
						},
					},
					failAfter: 1,
					failError: fmt.Errorf("connection lost"),
				}, nil
			}

			// Check that reconnect uses the last progress
			if in.After != "progress-1" {
				t.Errorf("Expected after='progress-1', got '%s'", in.After)
			}

			// Second call: succeed
			return &mockSubscribeClient{
				events: []*pullerv1.PullerEvent{
					{
						ChangeEvent: &pullerv1.ChangeEvent{EventId: "evt-2"},
						Progress:    "progress-2",
					},
				},
				failAfter: -1,
			}, nil
		},
	}

	var stateChanges []ConnectionState
	var mu sync.Mutex

	c := &Client{
		address: "localhost:50051",
		client:  mockClient,
		logger:  slog.Default(),
		cfg: ClientConfig{
			InitialBackoff:    10 * time.Millisecond,
			MaxBackoff:        50 * time.Millisecond,
			BackoffMultiplier: 2.0,
			OnStateChange: func(state ConnectionState, err error) {
				mu.Lock()
				stateChanges = append(stateChanges, state)
				mu.Unlock()
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := c.Subscribe(ctx, "consumer-1", "")

	var eventIDs []string
	for evt := range ch {
		eventIDs = append(eventIDs, evt.Change.EventID)
		if len(eventIDs) >= 2 {
			cancel()
		}
	}

	// Should have received events from both connections
	require.Len(t, eventIDs, 2)
	assert.Equal(t, "evt-1", eventIDs[0])
	assert.Equal(t, "evt-2", eventIDs[1])

	// Should have seen reconnecting state
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, stateChanges, StateReconnecting)
}

func TestClient_Subscribe_MaxRetries(t *testing.T) {
	t.Parallel()

	mockClient := &mockPullerServiceClient{
		subscribeFunc: func(ctx context.Context, in *pullerv1.SubscribeRequest, opts ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	var finalState ConnectionState
	var mu sync.Mutex

	c := &Client{
		address: "localhost:50051",
		client:  mockClient,
		logger:  slog.Default(),
		cfg: ClientConfig{
			InitialBackoff:    10 * time.Millisecond,
			MaxBackoff:        50 * time.Millisecond,
			BackoffMultiplier: 2.0,
			MaxRetries:        3,
			OnStateChange: func(state ConnectionState, err error) {
				mu.Lock()
				finalState = state
				mu.Unlock()
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := c.Subscribe(ctx, "consumer-1", "")

	// Channel should close after max retries
	for range ch {
		t.Error("Should not receive any events")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, StateDisconnected, finalState)
}

func TestClient_SubscribeWithCoalesce(t *testing.T) {
	t.Parallel()

	events := []*pullerv1.PullerEvent{
		{
			ChangeEvent: &pullerv1.ChangeEvent{EventId: "evt-1"},
			Progress:    "p1",
		},
	}

	mockClient := &mockPullerServiceClient{
		subscribeFunc: func(ctx context.Context, in *pullerv1.SubscribeRequest, opts ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
			if !in.CoalesceOnCatchUp {
				t.Error("Expected CoalesceOnCatchUp to be true")
			}
			return &mockSubscribeClient{events: events, failAfter: -1}, nil
		},
	}

	c := &Client{
		address: "localhost:50051",
		client:  mockClient,
		logger:  slog.Default(),
		cfg:     DefaultClientConfig(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch := c.SubscribeWithCoalesce(ctx, "consumer-1", "")

	select {
	case evt := <-ch:
		require.NotNil(t, evt)
		assert.Equal(t, "evt-1", evt.Change.EventID)
		cancel()
	case <-ctx.Done():
		t.Fatal("Timeout waiting for event")
	}
}

func TestClient_reconnect(t *testing.T) {
	t.Parallel()

	c := &Client{
		address: "localhost:50051",
		logger:  slog.Default(),
		cfg:     DefaultClientConfig(),
	}

	// First reconnect should succeed
	err := c.reconnect()
	require.NoError(t, err)
	require.NotNil(t, c.conn)
	require.NotNil(t, c.client)

	_ = c.Close()
}

func TestConnectionState_Values(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ConnectionState(0), StateConnected)
	assert.Equal(t, ConnectionState(1), StateReconnecting)
	assert.Equal(t, ConnectionState(2), StateDisconnected)
}

func TestClient_Subscribe_ContextCanceled(t *testing.T) {
	t.Parallel()

	events := []*pullerv1.PullerEvent{
		{
			ChangeEvent: &pullerv1.ChangeEvent{EventId: "evt-1"},
			Progress:    "p1",
		},
	}

	mockClient := &mockPullerServiceClient{
		subscribeFunc: func(ctx context.Context, in *pullerv1.SubscribeRequest, opts ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
			return &mockSubscribeClient{events: events, failAfter: -1}, nil
		},
	}

	var finalState ConnectionState
	var mu sync.Mutex

	c := &Client{
		address: "localhost:50051",
		client:  mockClient,
		logger:  slog.Default(),
		cfg: ClientConfig{
			InitialBackoff:    10 * time.Millisecond,
			MaxBackoff:        50 * time.Millisecond,
			BackoffMultiplier: 2.0,
			OnStateChange: func(state ConnectionState, err error) {
				mu.Lock()
				finalState = state
				mu.Unlock()
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	ch := c.Subscribe(ctx, "consumer-1", "")

	// Read one event then cancel
	<-ch
	cancel()

	// Wait for channel to close
	for range ch {
		// drain
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, StateDisconnected, finalState)
}

// contextCanceledSubscribeClient returns context.Canceled after receiving cancel signal
type contextCanceledSubscribeClient struct {
	grpc.ClientStream
	events     []*pullerv1.PullerEvent
	index      int
	mu         sync.Mutex
	ctx        context.Context
	sentEvents int
}

func (m *contextCanceledSubscribeClient) Recv() (*pullerv1.PullerEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// If context is canceled, return a context error
	if m.ctx.Err() != nil {
		return nil, m.ctx.Err()
	}

	if m.index >= len(m.events) {
		// Block until context is canceled
		m.mu.Unlock()
		<-m.ctx.Done()
		m.mu.Lock()
		return nil, m.ctx.Err()
	}
	evt := m.events[m.index]
	m.index++
	m.sentEvents++
	return evt, nil
}

func TestClient_Subscribe_ContextCanceled_DuringRecv(t *testing.T) {
	t.Parallel()

	events := []*pullerv1.PullerEvent{
		{
			ChangeEvent: &pullerv1.ChangeEvent{EventId: "evt-1"},
			Progress:    "p1",
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	mockClient := &mockPullerServiceClient{
		subscribeFunc: func(innerCtx context.Context, in *pullerv1.SubscribeRequest, opts ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
			return &contextCanceledSubscribeClient{events: events, ctx: innerCtx}, nil
		},
	}

	var finalState ConnectionState
	var mu sync.Mutex

	c := &Client{
		address: "localhost:50051",
		client:  mockClient,
		logger:  slog.Default(),
		cfg: ClientConfig{
			InitialBackoff:    10 * time.Millisecond,
			MaxBackoff:        50 * time.Millisecond,
			BackoffMultiplier: 2.0,
			OnStateChange: func(state ConnectionState, err error) {
				mu.Lock()
				finalState = state
				mu.Unlock()
			},
		},
	}

	ch := c.Subscribe(ctx, "consumer-1", "")

	// Read one event
	evt := <-ch
	require.NotNil(t, evt)

	// Cancel context while Recv is waiting
	cancel()

	// Wait for channel to close
	for range ch {
		// drain
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, StateDisconnected, finalState)
}
