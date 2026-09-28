package grpc

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

type retainedReplaySource struct {
	mu          sync.Mutex
	history     []*events.StoreChangeEvent
	replayCalls int
}

type connectedLogGate struct {
	next    slog.Handler
	entered chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (h *connectedLogGate) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *connectedLogGate) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "subscriber connected" {
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	return h.next.Handle(ctx, record)
}

func (h *connectedLogGate) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &connectedLogGate{next: h.next.WithAttrs(attrs), entered: h.entered, release: h.release, once: h.once}
}

func (h *connectedLogGate) WithGroup(name string) slog.Handler {
	return &connectedLogGate{next: h.next.WithGroup(name), entered: h.entered, release: h.release, once: h.once}
}

func (s *retainedReplaySource) SetEventHandler(func(context.Context, string, *events.StoreChangeEvent) error) {
}

func (s *retainedReplaySource) Replay(_ context.Context, after map[string]string, _ bool) (events.Iterator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replayCalls++
	start := 0
	for index, event := range s.history {
		if event.EventID == after["source"] {
			start = index + 1
			break
		}
	}
	replay := append([]*events.StoreChangeEvent(nil), s.history[start:]...)
	return &controllableIterator{events: replay}, nil
}

func (s *retainedReplaySource) ReplayFromAdmission(_ context.Context, after map[string]string, firstBroadcast map[string]events.ClusterTime) (events.Iterator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replayCalls++
	var replay []*events.StoreChangeEvent
	for _, event := range s.history {
		floor, present := firstBroadcast[event.Backend]
		if !present || event.ClusterTime.Compare(floor) < 0 {
			continue
		}
		replay = append(replay, event)
	}
	return &controllableIterator{events: replay}, nil
}

func (s *retainedReplaySource) BootstrapBoundary(context.Context) (string, error) { return "", nil }
func (s *retainedReplaySource) ValidateBoundary(context.Context, string) error    { return nil }
func (s *retainedReplaySource) ReplayBoundary(ctx context.Context, progress string, coalesce bool) (events.Iterator, error) {
	marker, err := cursor.DecodeProgressMarker(progress)
	if err != nil {
		return nil, err
	}
	return s.Replay(ctx, marker.Positions, coalesce)
}

func (s *retainedReplaySource) append(event *events.StoreChangeEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, event)
}

func (s *retainedReplaySource) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replayCalls
}

type blockingWarnState struct {
	once        sync.Once
	releaseOnce sync.Once
	entered     chan struct{}
	release     chan struct{}
}

func (s *blockingWarnState) unblock() {
	s.releaseOnce.Do(func() { close(s.release) })
}

type blockingWarnHandler struct {
	next  slog.Handler
	state *blockingWarnState
}

func newBlockingWarnLogger() (*slog.Logger, *blockingWarnState) {
	state := &blockingWarnState{entered: make(chan struct{}), release: make(chan struct{})}
	handler := &blockingWarnHandler{
		next:  slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}),
		state: state,
	}
	return slog.New(handler), state
}

func (h *blockingWarnHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *blockingWarnHandler) Handle(ctx context.Context, record slog.Record) error {
	blocked := false
	h.state.once.Do(func() {
		blocked = true
		close(h.state.entered)
	})
	if blocked {
		select {
		case <-h.state.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return h.next.Handle(ctx, record)
}

func (h *blockingWarnHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &blockingWarnHandler{next: h.next.WithAttrs(attrs), state: h.state}
}

func (h *blockingWarnHandler) WithGroup(name string) slog.Handler {
	return &blockingWarnHandler{next: h.next.WithGroup(name), state: h.state}
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

func (m *controllableEventSource) ReplayFromAdmission(ctx context.Context, after map[string]string, firstBroadcast map[string]events.ClusterTime) (events.Iterator, error) {
	return m.Replay(ctx, after, false)
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
	replayEvents := []*events.StoreChangeEvent{
		{EventID: "evt-2", Backend: "db1", ClusterTime: events.ClusterTime{T: 101, I: 1}},
		{EventID: "evt-3", Backend: "db1", ClusterTime: events.ClusterTime{T: 102, I: 1}},
	}
	source := &controllableEventSource{
		replayFunc: func(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
			mu.Lock()
			replayCount++
			mu.Unlock()
			return &controllableIterator{events: replayEvents}, nil
		},
	}
	logger, blockedWarn := newBlockingWarnLogger()
	t.Cleanup(blockedWarn.unblock)
	server := NewServer(cfg, source, logger)
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
		return event.ChangeEvent != nil && event.ChangeEvent.EventId == "evt-3"
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

	server.subs.Broadcast(&events.StoreChangeEvent{EventID: "evt-2", Backend: "db1", ClusterTime: events.ClusterTime{T: 101}})
	overflowDone := make(chan struct{})
	go func() {
		defer close(overflowDone)
		server.subs.Broadcast(&events.StoreChangeEvent{EventID: "evt-3", Backend: "db1", ClusterTime: events.ClusterTime{T: 102}})
	}()
	select {
	case <-blockedWarn.entered:
	case <-ctx.Done():
		t.Fatal("Timeout waiting for overflow logging to block")
	}
	close(sendBlock)

	select {
	case <-replayDone:
	case <-ctx.Done():
		t.Fatal("Timeout waiting for replay event after overflow")
	}
	blockedWarn.unblock()
	select {
	case <-overflowDone:
	case <-ctx.Done():
		t.Fatal("Timeout waiting for overflow broadcast to finish")
	}
	mu.Lock()
	if replayCount < 1 {
		t.Errorf("Expected at least 1 replay, got %d", replayCount)
	}
	mu.Unlock()
	stream.AssertExpectations(t)
}

func TestServerStartFromNowOverflowBeforeFirstDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gate := &connectedLogGate{
		next:    slog.NewTextHandler(io.Discard, nil),
		entered: make(chan struct{}),
		release: make(chan struct{}),
		once:    &sync.Once{},
	}
	t.Cleanup(func() {
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	})
	newEvent := func(backend, id, doc string, second uint32, operation events.StoreOperationType) *events.StoreChangeEvent {
		return &events.StoreChangeEvent{
			Backend: backend, EventID: id, MgoColl: "items", MgoDocID: doc,
			ClusterTime: events.ClusterTime{T: second, I: 1}, OpType: operation,
		}
	}
	old := newEvent("a", "old-a", "shared", 1, events.StoreOperationInsert)
	oldSibling := newEvent("a", "old-sibling", "prior", 2, events.StoreOperationUpdate)
	quietOld := newEvent("b", "old-b", "quiet", 1, events.StoreOperationInsert)
	first := newEvent("a", "first", "shared", 2, events.StoreOperationDelete)
	second := newEvent("a", "second", "other", 2, events.StoreOperationUpdate)
	quietNew := newEvent("b", "quiet-new", "quiet", 3, events.StoreOperationUpdate)
	source := &retainedReplaySource{history: []*events.StoreChangeEvent{old, oldSibling, quietOld}}
	server := NewServer(config.GRPCConfig{ChannelSize: 1}, source, slog.New(gate))
	server.Init()
	defer server.Shutdown()

	received := make(chan string, 8)
	stream := &mockStream{ctx: ctx, t: t}
	stream.On("Send", mock.Anything).Run(func(args mock.Arguments) {
		evt := args.Get(0).(*pullerv1.PullerEvent)
		if evt.ChangeEvent != nil {
			received <- evt.ChangeEvent.EventId
		}
	}).Return(nil)
	done := make(chan error, 1)
	go func() {
		done <- server.Subscribe(&pullerv1.SubscribeRequest{ConsumerId: "current-head", CoalesceOnCatchUp: true}, stream)
	}()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("subscriber did not register")
	}
	for _, evt := range []*events.StoreChangeEvent{first, second, quietNew} {
		source.append(evt)
		server.subs.Broadcast(evt)
	}
	require.True(t, server.subs.All()[0].RecoveryPending())
	close(gate.release)

	var delivered []string
	for len(delivered) < 3 {
		select {
		case id := <-received:
			delivered = append(delivered, id)
		case <-ctx.Done():
			t.Fatal("subscription did not recover before timeout")
		}
	}
	cancel()
	require.NoError(t, <-done)
	require.ElementsMatch(t, []string{first.EventID, second.EventID, quietNew.EventID}, delivered)
	require.Empty(t, received)
}

func TestServer_RepeatedRecoverySendsReadyOnce(t *testing.T) {
	source := &retainedReplaySource{}
	server := NewServer(config.GRPCConfig{ChannelSize: 1}, source, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ready := make(chan struct{})
	firstSend := make(chan struct{})
	firstRelease := make(chan struct{})
	firstRecovery := make(chan struct{})
	secondSend := make(chan struct{})
	secondRelease := make(chan struct{})
	secondRecovery := make(chan struct{})
	var sentMu sync.Mutex
	readyCount := 0
	delivered := make([]string, 0, 6)
	stream := &mockSubscribeServer{ctx: ctx, sendFunc: func(frame *pullerv1.PullerEvent) error {
		if frame.Ready {
			sentMu.Lock()
			readyCount++
			sentMu.Unlock()
			close(ready)
			return nil
		}
		id := frame.ChangeEvent.EventId
		sentMu.Lock()
		delivered = append(delivered, id)
		sentMu.Unlock()
		switch id {
		case "1-1-a":
			close(firstSend)
			<-firstRelease
		case "3-1-d":
			close(secondSend)
			<-secondRelease
		case "2-2-c":
			close(firstRecovery)
		case "4-2-f":
			close(secondRecovery)
		}
		return nil
	}}
	marker := cursor.NewProgressMarker()
	marker.Positions["source"] = "0-0-start"
	subscribeDone := make(chan error, 1)
	go func() {
		subscribeDone <- server.Subscribe(&pullerv1.SubscribeRequest{
			ConsumerId:   "repeated-recovery",
			After:        marker.Encode(),
			RequireReady: true,
		}, stream)
	}()

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("initial replay did not reach Ready")
	}

	broadcast := func(event *events.StoreChangeEvent) {
		source.append(event)
		server.subs.Broadcast(event)
	}
	broadcast(&events.StoreChangeEvent{Backend: "source", EventID: "1-1-a", ClusterTime: events.ClusterTime{T: 1, I: 1}})
	select {
	case <-firstSend:
	case <-ctx.Done():
		t.Fatal("first live send did not start")
	}
	broadcast(&events.StoreChangeEvent{Backend: "source", EventID: "2-1-b", ClusterTime: events.ClusterTime{T: 2, I: 1}})
	broadcast(&events.StoreChangeEvent{Backend: "source", EventID: "2-2-c", ClusterTime: events.ClusterTime{T: 2, I: 2}})
	close(firstRelease)
	select {
	case <-firstRecovery:
	case <-ctx.Done():
		t.Fatal("first recovery did not replay retained history")
	}

	broadcast(&events.StoreChangeEvent{Backend: "source", EventID: "3-1-d", ClusterTime: events.ClusterTime{T: 3, I: 1}})
	select {
	case <-secondSend:
	case <-ctx.Done():
		t.Fatal("second live send did not start")
	}
	broadcast(&events.StoreChangeEvent{Backend: "source", EventID: "4-1-e", ClusterTime: events.ClusterTime{T: 4, I: 1}})
	broadcast(&events.StoreChangeEvent{Backend: "source", EventID: "4-2-f", ClusterTime: events.ClusterTime{T: 4, I: 2}})
	close(secondRelease)
	select {
	case <-secondRecovery:
	case <-ctx.Done():
		t.Fatal("second recovery did not replay retained history")
	}
	cancel()
	select {
	case err := <-subscribeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("subscription did not stop after cancellation")
	}

	sentMu.Lock()
	require.Equal(t, 1, readyCount)
	require.Equal(t, []string{"1-1-a", "2-1-b", "2-2-c", "3-1-d", "4-1-e", "4-2-f"}, delivered)
	sentMu.Unlock()
	require.Equal(t, 3, source.calls())
}

func TestServer_RecoveryPendingTerminatesOnCancellationOrClosure(t *testing.T) {
	for _, phase := range []string{"cancel", "close"} {
		t.Run(phase, func(t *testing.T) {
			source := &controllableEventSource{}
			server := NewServer(config.GRPCConfig{ChannelSize: 1}, source, nil)
			server.Init()
			t.Cleanup(server.Shutdown)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream := &mockStream{ctx: ctx, t: t}
			sendEntered := make(chan struct{})
			sendBlock := make(chan struct{})
			stream.On("Send", mock.MatchedBy(func(event *pullerv1.PullerEvent) bool {
				return event.ChangeEvent != nil && event.ChangeEvent.EventId == "evt-1"
			})).Run(func(mock.Arguments) {
				close(sendEntered)
				<-sendBlock
			}).Return(nil).Once()

			subscribeDone := make(chan error, 1)
			go func() {
				subscribeDone <- server.Subscribe(&pullerv1.SubscribeRequest{ConsumerId: phase}, stream)
			}()
			require.Eventually(t, func() bool { return server.subs.Count() == 1 }, time.Second, time.Millisecond)
			server.subs.Broadcast(&events.StoreChangeEvent{EventID: "evt-1", Backend: "db1", ClusterTime: events.ClusterTime{T: 1}})
			select {
			case <-sendEntered:
			case <-ctx.Done():
				t.Fatal("first send did not block")
			}
			server.subs.Broadcast(&events.StoreChangeEvent{EventID: "evt-2", Backend: "db1", ClusterTime: events.ClusterTime{T: 2}})
			server.subs.Broadcast(&events.StoreChangeEvent{EventID: "evt-3", Backend: "db1", ClusterTime: events.ClusterTime{T: 3}})
			sub := server.subs.All()[0]
			require.True(t, sub.RecoveryPending())

			if phase == "cancel" {
				cancel()
			} else {
				server.subs.CloseAll()
			}
			close(sendBlock)
			select {
			case err := <-subscribeDone:
				if phase == "cancel" {
					require.NoError(t, err)
				} else {
					require.Equal(t, codes.Canceled, status.Code(err))
				}
			case <-time.After(time.Second):
				t.Fatal("subscription did not terminate with recovery pending")
			}
			stream.AssertExpectations(t)
		})
	}
}

func TestServer_Boundary_EmptyReplay(t *testing.T) {
	cfg := config.GRPCConfig{ChannelSize: 100}
	replayCalled := make(chan map[string]string, 1)
	source := &controllableEventSource{
		replayFunc: func(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
			replayCalled <- after
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
		return event.ChangeEvent != nil && event.ChangeEvent.EventId == "2-1-live"
	})).Run(func(mock.Arguments) { close(done) }).Return(nil).Once()

	marker := makeProgressMarker("db1", "1-1-replayed")
	go func() {
		req := &pullerv1.SubscribeRequest{ConsumerId: "empty-replay", After: marker}
		server.Subscribe(req, stream)
	}()

	select {
	case after := <-replayCalled:
		require.Equal(t, "1-1-replayed", after["db1"])
	case <-ctx.Done():
		t.Fatal("Timeout waiting for the empty replay")
	}
	require.Eventually(t, func() bool { return server.SubscriberCount() == 1 }, time.Second, time.Millisecond)

	liveEvt := &events.StoreChangeEvent{EventID: "2-1-live", ClusterTime: events.ClusterTime{T: 2, I: 1}}
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
