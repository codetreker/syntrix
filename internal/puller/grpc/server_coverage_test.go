package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mockSubscribeServer struct {
	grpc.ServerStream
	ctx      context.Context
	sendFunc func(*pullerv1.PullerEvent) error
}

func (m *mockSubscribeServer) Context() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

func (m *mockSubscribeServer) Send(evt *pullerv1.PullerEvent) error {
	if m.sendFunc != nil {
		return m.sendFunc(evt)
	}
	return nil
}

func TestServer_InitShutdown(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{
		MaxConnections: 10,
	}

	source := &mockEventSource{}
	srv := NewServer(cfg, source, nil)

	// Init server
	srv.Init()

	// Verify handler was set
	if source.handler == nil {
		t.Error("Expected handler to be set after Init")
	}

	// Shutdown server
	srv.Shutdown()
}

func TestServer_Init_AlreadyInitialized(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{}
	srv := NewServer(cfg, &mockEventSource{}, nil)

	// Init first time
	srv.Init()

	// Init second time should be idempotent
	srv.Init()

	// Should not panic or cause issues
	srv.Shutdown()
}

func TestServer_Subscribe(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{
		MaxConnections: 10,
	}

	source := &mockEventSource{}
	srv := NewServer(cfg, source, nil)
	srv.Init()
	defer srv.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Create mock stream
	eventReceived := make(chan *pullerv1.PullerEvent, 1)
	stream := &mockSubscribeServer{
		ctx: ctx,
		sendFunc: func(evt *pullerv1.PullerEvent) error {
			eventReceived <- evt
			return nil
		},
	}

	// Run Subscribe in background
	errChan := make(chan error)
	go func() {
		errChan <- srv.Subscribe(&pullerv1.SubscribeRequest{ConsumerId: "test-consumer"}, stream)
	}()

	// Wait for subscriber to be added
	time.Sleep(100 * time.Millisecond)

	// Send an event via source handler
	if source.handler != nil {
		evt := &events.StoreChangeEvent{
			EventID: "evt-1",
			MgoColl: "users",
			Backend: "backend1",
		}
		_ = source.handler(ctx, "backend1", evt)
	} else {
		t.Fatal("Handler not set")
	}

	// Receive event
	select {
	case resp := <-eventReceived:
		if resp.ChangeEvent.EventId != "evt-1" {
			t.Errorf("Expected event ID evt-1, got %s", resp.ChangeEvent.EventId)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for event")
	}
}

func TestServer_Subscribe_ConsumerIDDoesNotIdentifyConnection(t *testing.T) {
	for _, consumerID := range []string{"shared-consumer", ""} {
		for _, canceled := range []int{0, 1} {
			t.Run(fmt.Sprintf("consumer=%q/cancel=%d", consumerID, canceled), func(t *testing.T) {
				source := &mockEventSource{}
				srv := NewServer(config.GRPCConfig{ChannelSize: 4, HeartbeatInterval: time.Hour}, source, nil)
				srv.Init()
				t.Cleanup(srv.Shutdown)

				var cancels [2]context.CancelFunc
				var received [2]chan *pullerv1.PullerEvent
				var results [2]chan error
				for i := range 2 {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					cancels[i] = cancel
					messages := make(chan *pullerv1.PullerEvent, 2)
					received[i] = messages
					result := make(chan error, 1)
					results[i] = result
					exited := make(chan struct{})
					stream := &mockSubscribeServer{
						ctx: ctx,
						sendFunc: func(evt *pullerv1.PullerEvent) error {
							select {
							case messages <- evt:
								return nil
							case <-ctx.Done():
								return ctx.Err()
							}
						},
					}
					t.Cleanup(func() {
						cancel()
						select {
						case <-exited:
						case <-time.After(2 * time.Second):
							t.Error("Subscribe handler did not exit")
						}
					})
					go func() {
						defer close(exited)
						result <- srv.Subscribe(&pullerv1.SubscribeRequest{ConsumerId: consumerID}, stream)
					}()
					require.Eventually(t, func() bool {
						return srv.SubscriberCount() == i+1
					}, time.Second, time.Millisecond)
				}

				first := &events.StoreChangeEvent{
					EventID: "event-1", Backend: "backend", ClusterTime: events.ClusterTime{T: 1, I: 1},
				}
				require.NoError(t, source.EmitEvent(context.Background(), first.Backend, first))
				for _, messages := range received {
					select {
					case response := <-messages:
						require.Equal(t, first.EventID, response.GetChangeEvent().GetEventId())
					case <-time.After(time.Second):
						t.Fatal("registered subscriber did not receive the first event")
					}
				}

				cancels[canceled]()
				select {
				case err := <-results[canceled]:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatal("canceled Subscribe handler did not complete its deferred removal")
				}
				require.Equal(t, 1, srv.SubscriberCount())

				second := &events.StoreChangeEvent{
					EventID: "event-2", Backend: "backend", ClusterTime: events.ClusterTime{T: 1, I: 2},
				}
				require.NoError(t, source.EmitEvent(context.Background(), second.Backend, second))
				remaining := 1 - canceled
				select {
				case response := <-received[remaining]:
					require.Equal(t, second.EventID, response.GetChangeEvent().GetEventId())
				case <-time.After(time.Second):
					t.Fatal("remaining subscriber did not receive the event after the other handler exited")
				}
				cancels[remaining]()
				select {
				case err := <-results[remaining]:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatal("remaining Subscribe handler did not exit")
				}
				require.Zero(t, srv.SubscriberCount())
			})
		}
	}
}

func TestServer_Subscribe_SendError(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{MaxConnections: 1}, &mockEventSource{}, nil)
	t.Cleanup(srv.cancel)
	go srv.processEvents()

	req := &pullerv1.SubscribeRequest{ConsumerId: "c1"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := &mockSubscribeServer{
		ctx: ctx,
		sendFunc: func(evt *pullerv1.PullerEvent) error {
			return fmt.Errorf("send failed")
		},
	}

	// Run Subscribe in goroutine
	errChan := make(chan error)
	go func() {
		errChan <- srv.Subscribe(req, stream)
	}()

	// Wait for subscriber to be added
	time.Sleep(100 * time.Millisecond)

	// Broadcast event
	evt := &events.StoreChangeEvent{EventID: "evt1", Backend: "backend1"}
	srv.eventChan <- evt

	// Subscribe should return error
	select {
	case err := <-errChan:
		if err == nil || err.Error() != "send failed" {
			t.Errorf("Expected 'send failed' error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for Subscribe to return")
	}
	assertAdmissionSlotReusable(t, srv)
}

func TestServer_Subscribe_SubscriberClosed(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	req := &pullerv1.SubscribeRequest{ConsumerId: "c1"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := &mockSubscribeServer{ctx: ctx}

	// Run Subscribe
	errChan := make(chan error)
	go func() {
		errChan <- srv.Subscribe(req, stream)
	}()

	time.Sleep(100 * time.Millisecond)

	// Send nil event to events channel
	srv.eventChan <- nil
}

func TestServer_Subscribe_NilEvent(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	go srv.processEvents()

	req := &pullerv1.SubscribeRequest{ConsumerId: "c1"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := &mockSubscribeServer{ctx: ctx}

	go func() {
		_ = srv.Subscribe(req, stream)
	}()
	time.Sleep(50 * time.Millisecond)

	// Send nil event - should be ignored
	srv.eventChan <- nil

	// Send valid event to ensure it's still running
	done := make(chan struct{})
	stream.sendFunc = func(e *pullerv1.PullerEvent) error {
		close(done)
		return nil
	}

	srv.eventChan <- &events.StoreChangeEvent{
		Backend: "b1",
		EventID: "1",
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for event after nil")
	}
}

func TestServer_SendEvent_ConvertErrorPreservesProgress(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	sub := testSubscriber(t, "consumer", cursor.NewProgressMarker(), false, 1)
	stream := &mockSubscribeServer{ctx: context.Background()}
	doc := storage.NewStoredDoc("database", "users", "doc", map[string]any{"bad": make(chan int)})
	err := srv.sendEvent(stream, sub, "backend", &events.StoreChangeEvent{EventID: "bad", FullDocument: &doc})
	require.Error(t, err)
	require.Empty(t, sub.CurrentProgress().Positions)
}

func TestServer_ResponseSizeAdmissionPreservesProgress(t *testing.T) {
	server := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	position := cursor.NewProgressMarker()
	position.SetPosition("other", "1-1-original")
	position.Lineages = map[string]string{"other": strings.Repeat("x", events.MaxRPCBytes)}
	sub := testSubscriber(t, "bounded", position, false, 1)
	stream := &mockSubscribeServer{ctx: context.Background(), sendFunc: func(*pullerv1.PullerEvent) error {
		t.Fatal("oversized response reached transport")
		return nil
	}}
	err := server.sendEvent(stream, sub, "backend", &events.StoreChangeEvent{EventID: "next"})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Empty(t, sub.CurrentProgress().GetPosition("backend"))
	err = server.sendHeartbeat(stream, sub)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	err = validateResponseSize(&pullerv1.BoundaryResponse{Progress: strings.Repeat("x", events.MaxRPCBytes)})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	_, err = server.convertEvent("backend", &events.StoreChangeEvent{EventID: strings.Repeat("x", events.MaxEventBytes)})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestServer_Subscribe_ContextCancellation(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	go srv.processEvents()

	req := &pullerv1.SubscribeRequest{ConsumerId: "ctx-cancel-test"}
	ctx, cancel := context.WithCancel(context.Background())
	stream := &mockSubscribeServer{ctx: ctx}

	errChan := make(chan error)
	go func() {
		errChan <- srv.Subscribe(req, stream)
	}()

	// Wait for subscription to be established
	time.Sleep(50 * time.Millisecond)

	// Cancel context - should trigger ctx.Done() branch
	cancel()

	select {
	case err := <-errChan:
		// Should return nil on graceful context cancellation
		if err != nil {
			t.Logf("Subscribe returned: %v (expected nil or canceled)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for Subscribe to return after context cancellation")
	}
}

func TestServer_Subscribe_SubscriberClosed_Live(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	go srv.processEvents()

	req := &pullerv1.SubscribeRequest{ConsumerId: "sub-close-test"}
	ctx := context.Background()
	stream := &mockSubscribeServer{ctx: ctx}

	errChan := make(chan error)
	go func() {
		errChan <- srv.Subscribe(req, stream)
	}()

	// Wait for subscription to be established in live mode
	time.Sleep(50 * time.Millisecond)

	// Close all subscribers - this should trigger sub.Done() branch
	srv.subs.CloseAll()

	select {
	case err := <-errChan:
		// Should return error when subscriber is closed
		if err == nil {
			t.Error("Expected error when subscriber is closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for Subscribe to return after subscriber closed")
	}
}

func TestServer_Shutdown_NotInitialized(t *testing.T) {
	t.Parallel()
	cfg := config.GRPCConfig{
		MaxConnections: 10,
	}

	source := &mockEventSource{}
	srv := NewServer(cfg, source, nil)

	// Shutdown without Init should be safe
	srv.Shutdown()
}

func TestServer_processEvents_ChannelClosed(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)

	// Close event channel to trigger !ok branch
	close(srv.eventChan)

	// processEvents should return when channel is closed
	done := make(chan struct{})
	go func() {
		srv.processEvents()
		close(done)
	}()

	select {
	case <-done:
		// Good - processEvents returned
	case <-time.After(time.Second):
		t.Fatal("processEvents did not return after channel closed")
	}
}

func TestServer_processEvents_ContextDone(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	srv.ctx = ctx

	done := make(chan struct{})
	go func() {
		srv.processEvents()
		close(done)
	}()

	// Cancel context to trigger ctx.Done() branch
	cancel()

	select {
	case <-done:
		// Good - processEvents returned
	case <-time.After(time.Second):
		t.Fatal("processEvents did not return after context cancelled")
	}
}

// replayErrorEventSource returns an error from Replay()
type replayErrorEventSource struct {
	mockEventSource
	replayErr error
}

func (r *replayErrorEventSource) Replay(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
	return nil, r.replayErr
}

// makeProgressMarker creates a base64-encoded progress marker for testing
func makeProgressMarker(backend, eventID string) string {
	pm := map[string]interface{}{
		"p": map[string]string{backend: eventID},
	}
	data, _ := json.Marshal(pm)
	return base64.RawURLEncoding.EncodeToString(data)
}

func TestServer_Subscribe_ReplayError(t *testing.T) {
	t.Parallel()

	source := &replayErrorEventSource{
		replayErr: fmt.Errorf("database connection failed"),
	}
	srv := NewServer(config.GRPCConfig{MaxConnections: 1}, source, nil)
	t.Cleanup(srv.cancel)
	go srv.processEvents()

	// Request with after position to trigger replay mode
	req := &pullerv1.SubscribeRequest{
		ConsumerId: "c1",
		After:      makeProgressMarker("backend1", "1-1-initial"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := &mockSubscribeServer{ctx: ctx}

	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.Subscribe(req, stream)
	}()

	select {
	case err := <-errChan:
		if err == nil {
			t.Error("Expected error from Replay failure")
		}
		if !containsSubstr(err.Error(), "failed to start replay") {
			t.Errorf("Expected 'failed to start replay' error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for Subscribe to return")
	}
	assertAdmissionSlotReusable(t, srv)
}

// iteratorWithError returns an error from Err()
type iteratorWithError struct {
	events []*events.StoreChangeEvent
	idx    int
	err    error
}

func (i *iteratorWithError) Next() bool {
	i.idx++
	return i.idx <= len(i.events)
}

func (i *iteratorWithError) Event() *events.StoreChangeEvent {
	if i.idx > 0 && i.idx <= len(i.events) {
		return i.events[i.idx-1]
	}
	return nil
}

func (i *iteratorWithError) Err() error   { return i.err }
func (i *iteratorWithError) Close() error { return nil }

type iteratorErrEventSource struct {
	mockEventSource
	iter *iteratorWithError
}

func (r *iteratorErrEventSource) Replay(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
	return r.iter, nil
}

func TestServer_Subscribe_IteratorError(t *testing.T) {
	t.Parallel()

	source := &iteratorErrEventSource{
		iter: &iteratorWithError{
			events: []*events.StoreChangeEvent{},
			err:    fmt.Errorf("cursor exhausted unexpectedly"),
		},
	}
	srv := NewServer(config.GRPCConfig{MaxConnections: 1}, source, nil)
	t.Cleanup(srv.cancel)
	go srv.processEvents()

	req := &pullerv1.SubscribeRequest{
		ConsumerId: "c1",
		After:      makeProgressMarker("backend1", "1-1-initial"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := &mockSubscribeServer{ctx: ctx}

	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.Subscribe(req, stream)
	}()

	select {
	case err := <-errChan:
		if err == nil {
			t.Error("Expected error from iterator error")
		}
		if !containsSubstr(err.Error(), "replay error") {
			t.Errorf("Expected 'replay error' error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for Subscribe to return")
	}
	assertAdmissionSlotReusable(t, srv)
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// iteratorWithEvents returns events and then optionally an error
type iteratorWithEvents struct {
	events []*events.StoreChangeEvent
	idx    int
	err    error
}

func (i *iteratorWithEvents) Next() bool {
	i.idx++
	return i.idx <= len(i.events)
}

func (i *iteratorWithEvents) Event() *events.StoreChangeEvent {
	if i.idx > 0 && i.idx <= len(i.events) {
		return i.events[i.idx-1]
	}
	return nil
}

func (i *iteratorWithEvents) Err() error   { return i.err }
func (i *iteratorWithEvents) Close() error { return nil }

type replayWithEventsSource struct {
	mockEventSource
	iter *iteratorWithEvents
}

func (r *replayWithEventsSource) Replay(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
	return r.iter, nil
}

func TestServer_Subscribe_SendEventError_DuringReplay(t *testing.T) {
	t.Parallel()

	// Create source that returns events during replay
	source := &replayWithEventsSource{
		iter: &iteratorWithEvents{
			events: []*events.StoreChangeEvent{
				{Backend: "backend1", EventID: "evt-1", ClusterTime: events.ClusterTime{T: 100, I: 1}},
			},
		},
	}
	srv := NewServer(config.GRPCConfig{MaxConnections: 1}, source, nil)
	t.Cleanup(srv.cancel)
	go srv.processEvents()

	req := &pullerv1.SubscribeRequest{
		ConsumerId: "c1",
		After:      makeProgressMarker("backend1", "1-1-initial"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sendErr := fmt.Errorf("network error: connection reset")
	stream := &mockSubscribeServer{
		ctx: ctx,
		sendFunc: func(evt *pullerv1.PullerEvent) error {
			return sendErr
		},
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.Subscribe(req, stream)
	}()

	select {
	case err := <-errChan:
		if err == nil {
			t.Error("Expected error from sendEvent failure during replay")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for Subscribe to return")
	}
	assertAdmissionSlotReusable(t, srv)
}

func TestServer_Subscribe_HeartbeatError(t *testing.T) {
	t.Parallel()

	source := &mockEventSource{}
	srv := NewServer(config.GRPCConfig{
		MaxConnections:    1,
		HeartbeatInterval: 50 * time.Millisecond, // Short heartbeat interval
	}, source, nil)
	t.Cleanup(srv.cancel)
	go srv.processEvents()

	req := &pullerv1.SubscribeRequest{
		ConsumerId: "c1",
		// No After, so it starts in live mode
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	heartbeatCount := 0
	stream := &mockSubscribeServer{
		ctx: ctx,
		sendFunc: func(evt *pullerv1.PullerEvent) error {
			heartbeatCount++
			if heartbeatCount >= 1 {
				return fmt.Errorf("heartbeat send failed")
			}
			return nil
		},
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.Subscribe(req, stream)
	}()

	select {
	case err := <-errChan:
		if err == nil {
			t.Error("Expected error from heartbeat failure")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Timeout waiting for Subscribe to return")
	}
	assertAdmissionSlotReusable(t, srv)
}

func TestServer_SubscriberCount(t *testing.T) {
	t.Parallel()
	srv := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)

	if srv.SubscriberCount() != 0 {
		t.Errorf("Expected 0 subscribers, got %d", srv.SubscriberCount())
	}
}
