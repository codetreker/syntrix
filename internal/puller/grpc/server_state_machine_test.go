package grpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// --- Mocks ---

type mockStream struct {
	mock.Mock
	grpc.ServerStream
	ctx context.Context
	t   *testing.T
}

func (m *mockStream) Context() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

func (m *mockStream) Send(event *pullerv1.PullerEvent) error {
	if m.t != nil {
		m.t.Logf("MockStream.Send called with event: %s", event.ChangeEvent.EventId)
	}
	args := m.Called(event)
	return args.Error(0)
}

func (m *mockStream) SetHeader(md metadata.MD) error {
	return nil
}

func (m *mockStream) SendHeader(md metadata.MD) error {
	return nil
}

func (m *mockStream) SetTrailer(md metadata.MD) {
}

type controllableIterator struct {
	events []*events.StoreChangeEvent
	idx    int
}

func (m *controllableIterator) Next() bool {
	m.idx++
	return m.idx <= len(m.events)
}

func (m *controllableIterator) Event() *events.StoreChangeEvent {
	if m.idx > 0 && m.idx <= len(m.events) {
		return m.events[m.idx-1]
	}
	return nil
}

func (m *controllableIterator) Err() error {
	return nil
}

func (m *controllableIterator) Close() error {
	return nil
}

type controllableEventSource struct {
	replayFunc func(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error)
	handler    func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error
}

func (m *controllableEventSource) SetEventHandler(handler func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error) {
	m.handler = handler
}

func (m *controllableEventSource) EmitEvent(ctx context.Context, backendName string, event *events.StoreChangeEvent) error {
	if m.handler != nil {
		return m.handler(ctx, backendName, event)
	}
	return nil
}

func (m *controllableEventSource) Replay(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
	if m.replayFunc != nil {
		return m.replayFunc(ctx, after, coalesce)
	}
	return &controllableIterator{events: nil}, nil
}

// --- Tests ---

func TestServer_StateMachine_HappyPath(t *testing.T) {
	// Scenario: Replay finishes -> Switch to Live -> Receive events

	// Setup
	cfg := config.GRPCConfig{ChannelSize: 100}
	replayEvt := &events.StoreChangeEvent{
		EventID:     "replay-1",
		ClusterTime: events.ClusterTime{T: 100, I: 1},
	}

	source := &controllableEventSource{
		replayFunc: func(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
			return &controllableIterator{events: []*events.StoreChangeEvent{replayEvt}}, nil
		},
	}
	server := NewServer(cfg, source, nil)

	// Init the server to start the event loop
	server.Init()
	defer server.Shutdown()

	// Mock Stream
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := &mockStream{ctx: ctx, t: t}

	// Expectation:
	// 1. Send replay event (NOT ANYMORE - we start in live mode if no cursor)
	// 2. Send live event

	done := make(chan struct{})

	// stream.On("Send", mock.MatchedBy(func(event *pullerv1.PullerEvent) bool {
	// 	// Check if it's the replay event
	// 	if event.ChangeEvent.EventId == "replay-1" {
	// 		return true
	// 	}
	// 	return false
	// })).Return(nil).Once()

	stream.On("Send", mock.MatchedBy(func(event *pullerv1.PullerEvent) bool {
		// Check if it's the live event
		if event.ChangeEvent.EventId == "live-1" {
			close(done) // Signal completion
			return true
		}
		return false
	})).Return(nil).Once()

	// Run Subscribe in a goroutine
	go func() {
		req := &pullerv1.SubscribeRequest{
			ConsumerId: "test-consumer",
			// After: "some-cursor", // If we wanted replay
		}
		server.Subscribe(req, stream)
	}()

	// Wait for replay to finish (it happens immediately in this mock)
	// We need to wait a bit for the server to switch to live mode and register the subscriber
	time.Sleep(100 * time.Millisecond)

	// Broadcast a live event via source
	liveEvt := &events.StoreChangeEvent{
		EventID:     "live-1",
		ClusterTime: events.ClusterTime{T: 200, I: 1},
	}
	source.EmitEvent(context.Background(), "db1", liveEvt)

	// Wait for test completion
	select {
	case <-done:
	// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for live event")
	}

	stream.AssertExpectations(t)
}

func TestServer_StateMachine_LiveDowngrade(t *testing.T) {
	cfg := config.GRPCConfig{ChannelSize: 1}
	var replayCount int
	var mu sync.Mutex
	replayEvt := &events.StoreChangeEvent{
		EventID: "replay-2", Backend: "db1", ClusterTime: events.ClusterTime{T: 103, I: 1},
	}
	source := &controllableEventSource{
		replayFunc: func(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
			mu.Lock()
			replayCount++
			mu.Unlock()
			return &controllableIterator{events: []*events.StoreChangeEvent{replayEvt}}, nil
		},
	}
	server := NewServer(cfg, source, nil)
	server.Init()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	stream := &mockStream{ctx: ctx, t: t}
	sendEntered := make(chan struct{})
	sendBlock := make(chan struct{})
	stream.On("Send", mock.MatchedBy(func(event *pullerv1.PullerEvent) bool {
		return event.ChangeEvent != nil && event.ChangeEvent.EventId == "evt-1"
	})).Run(func(args mock.Arguments) {
		close(sendEntered)
		select {
		case <-sendBlock:
		case <-ctx.Done():
		}
	}).Return(nil).Once()
	stream.On("Send", mock.MatchedBy(func(event *pullerv1.PullerEvent) bool {
		return event.ChangeEvent != nil && event.ChangeEvent.EventId == "evt-2"
	})).Return(nil).Once()
	replayDone := make(chan struct{})
	stream.On("Send", mock.MatchedBy(func(event *pullerv1.PullerEvent) bool {
		return event.ChangeEvent != nil && event.ChangeEvent.EventId == "replay-2"
	})).Run(func(args mock.Arguments) {
		close(replayDone)
	}).Return(nil).Once()

	subscribeDone := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-subscribeDone:
			if err != nil {
				t.Errorf("Subscribe returned an error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Timeout waiting for Subscribe to stop")
		}
		server.Shutdown()
	})
	go func() {
		subscribeDone <- server.Subscribe(&pullerv1.SubscribeRequest{ConsumerId: "overflow-consumer"}, stream)
	}()

	registrationPoll := time.NewTicker(time.Millisecond)
	defer registrationPoll.Stop()
	for server.subs.Count() != 1 {
		select {
		case <-registrationPoll.C:
		case <-ctx.Done():
			t.Fatal("Timeout waiting for subscriber registration")
		}
	}
	if err := source.EmitEvent(ctx, "db1", &events.StoreChangeEvent{EventID: "evt-1", ClusterTime: events.ClusterTime{T: 100}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sendEntered:
	case <-ctx.Done():
		t.Fatal("Timeout waiting for the first Send to block")
	}

	// The downgrade scenario requires overflow to be fully published before
	// Send resumes. EmitEvent only enqueues asynchronous broadcast work.
	server.subs.Broadcast(&events.StoreChangeEvent{EventID: "evt-2", Backend: "db1", ClusterTime: events.ClusterTime{T: 101}})
	server.subs.Broadcast(&events.StoreChangeEvent{EventID: "evt-3", Backend: "db1", ClusterTime: events.ClusterTime{T: 102}})
	close(sendBlock)

	select {
	case <-replayDone:
	case <-ctx.Done():
		t.Fatal("Timeout waiting for replay event after overflow")
	}
	mu.Lock()
	if replayCount < 1 {
		t.Errorf("Expected at least 1 replay, got %d", replayCount)
	}
	mu.Unlock()
	stream.AssertExpectations(t)
}

func TestServer_Boundary_EmptyReplay(t *testing.T) {
	// Scenario: Replay returns no events -> Immediate switch to Live

	cfg := config.GRPCConfig{ChannelSize: 100}
	source := &controllableEventSource{
		replayFunc: func(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
			return &controllableIterator{events: []*events.StoreChangeEvent{}}, nil
		},
	}
	server := NewServer(cfg, source, nil)
	server.Init()
	defer server.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := &mockStream{ctx: ctx, t: t}

	done := make(chan struct{})
	stream.On("Send", mock.MatchedBy(func(event *pullerv1.PullerEvent) bool {
		if event.ChangeEvent.EventId == "live-1" {
			close(done)
			return true
		}
		return false
	})).Return(nil).Once()

	go func() {
		req := &pullerv1.SubscribeRequest{ConsumerId: "empty-replay"}
		server.Subscribe(req, stream)
	}()

	time.Sleep(50 * time.Millisecond)

	// Emit live event
	liveEvt := &events.StoreChangeEvent{EventID: "live-1", ClusterTime: events.ClusterTime{T: 200}}
	source.EmitEvent(context.Background(), "db1", liveEvt)

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for live event")
	}
	stream.AssertExpectations(t)
}

func TestServer_Boundary_ImmediateCancel(t *testing.T) {
	// Scenario: Context cancelled before Replay starts

	cfg := config.GRPCConfig{ChannelSize: 100}
	source := &controllableEventSource{}
	server := NewServer(cfg, source, nil)
	server.Init()
	defer server.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	cancel() // Cancel immediately

	stream := &mockStream{ctx: ctx, t: t}

	req := &pullerv1.SubscribeRequest{ConsumerId: "cancel-consumer"}
	err := server.Subscribe(req, stream)

	// Subscribe returns nil on context cancellation (graceful disconnect)
	if err != nil {
		t.Errorf("Expected nil error on context cancellation, got: %v", err)
	}
}

func TestServer_Boundary_SendError(t *testing.T) {
	// Scenario: stream.Send returns error -> Should terminate subscription

	cfg := config.GRPCConfig{ChannelSize: 100}
	replayEvt := &events.StoreChangeEvent{EventID: "replay-1", ClusterTime: events.ClusterTime{T: 100}}

	source := &controllableEventSource{
		replayFunc: func(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
			return &controllableIterator{events: []*events.StoreChangeEvent{replayEvt}}, nil
		},
	}
	server := NewServer(cfg, source, nil)
	server.Init()
	defer server.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := &mockStream{ctx: ctx, t: t}

	// Send returns error
	stream.On("Send", mock.Anything).Return(context.Canceled).Once()

	go func() {
		time.Sleep(10 * time.Millisecond)
		source.EmitEvent(context.Background(), "db1", &events.StoreChangeEvent{EventID: "live-1", ClusterTime: events.ClusterTime{T: 200}})
	}()

	req := &pullerv1.SubscribeRequest{ConsumerId: "error-consumer"}
	err := server.Subscribe(req, stream)

	if err == nil {
		t.Error("Expected error from Subscribe when Send fails")
	}
	stream.AssertExpectations(t)
}

func TestServer_Boundary_InvalidMarker(t *testing.T) {
	// Scenario: Subscribe with malformed after token

	cfg := config.GRPCConfig{ChannelSize: 100}
	server := NewServer(cfg, &controllableEventSource{}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := &mockStream{ctx: ctx, t: t}

	req := &pullerv1.SubscribeRequest{
		ConsumerId: "invalid-marker",
		After:      "invalid-base64-token",
	}

	err := server.Subscribe(req, stream)
	if err == nil {
		t.Error("Expected error for invalid marker")
	}
}
