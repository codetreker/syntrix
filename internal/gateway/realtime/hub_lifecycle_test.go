package realtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type lifecycleStream struct {
	mu          sync.Mutex
	status      streamer.StreamStatus
	changed     chan struct{}
	subscribe   func(context.Context) (streamer.Registration, error)
	unsubscribe func(string) error
	closed      atomic.Int32
	next        atomic.Uint64
}

func newLifecycleStream() *lifecycleStream {
	changed := make(chan struct{})
	return &lifecycleStream{changed: changed, status: streamer.StreamStatus{State: streamer.StateConnected, Generation: 1, Changed: changed}}
}
func (s *lifecycleStream) Subscribe(ctx context.Context, _ string, _ string, _ []model.Filter) (streamer.Registration, error) {
	if s.subscribe != nil {
		return s.subscribe(ctx)
	}
	return streamer.Registration{ID: string(rune('a' + s.next.Add(1))), Generation: s.Status().Generation}, nil
}
func (s *lifecycleStream) Status() streamer.StreamStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}
func (s *lifecycleStream) Unsubscribe(id string) error {
	if s.unsubscribe != nil {
		return s.unsubscribe(id)
	}
	return nil
}
func (s *lifecycleStream) Recv() (*streamer.EventDelivery, error) { return nil, io.EOF }
func (s *lifecycleStream) Close() error                           { s.closed.Add(1); return nil }
func (s *lifecycleStream) transition(state streamer.ConnectionState, generation uint64) streamer.StreamStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.status = streamer.StreamStatus{State: state, Generation: generation, Changed: s.changed}
	return s.status
}

func TestHubReplicaLateACKIsCleanedOnOriginalStream(t *testing.T) {
	h := NewHub()
	stream := newLifecycleStream()
	h.SetStream(stream)
	entered, finish := make(chan struct{}), make(chan struct{})
	stream.subscribe = func(ctx context.Context) (streamer.Registration, error) {
		close(entered)
		<-finish
		return streamer.Registration{ID: "late", Generation: 1}, nil
	}
	cleaned := make(chan string, 1)
	stream.unsubscribe = func(id string) error { cleaned <- id; return nil }
	var invalidated, connections atomic.Int32
	_, ok := h.RegisterReplicaConnection(func() { connections.Add(1) })
	require.True(t, ok)
	result := make(chan error, 1)
	go func() {
		_, err := h.SubscribeReplica(context.Background(), "db", "users", func() { t.Error("late registration delivered") }, func(error) { invalidated.Add(1) })
		result <- err
	}()
	<-entered
	h.InvalidateReplicaStream(stream, stream.transition(streamer.StateReconnecting, 2))
	require.EqualValues(t, 1, invalidated.Load())
	require.EqualValues(t, 1, connections.Load())
	close(finish)
	require.ErrorIs(t, <-result, errReplicaStreamChanged)
	select {
	case id := <-cleaned:
		require.Equal(t, "late", id)
	case <-time.After(time.Second):
		t.Fatal("late ACK was not cleaned")
	}
	h.InvalidateReplicaStream(stream, stream.Status())
	require.EqualValues(t, 1, invalidated.Load())
}

func TestHubReplicaGenerationKeepsNewOwnersAndRetiresOldOnes(t *testing.T) {
	h := NewHub()
	stream := newLifecycleStream()
	h.SetStream(stream)
	var old, fresh atomic.Int32
	_, ok := h.RegisterReplicaConnection(func() { old.Add(1) })
	require.True(t, ok)
	status := stream.transition(streamer.StateConnected, 2)
	release, ok := h.RegisterReplicaConnection(func() { fresh.Add(1) })
	require.True(t, ok)
	defer release()
	h.InvalidateReplicaStream(stream, status)
	require.EqualValues(t, 1, old.Load())
	require.Zero(t, fresh.Load())
	h.SetStream(newLifecycleStream())
	require.EqualValues(t, 1, fresh.Load())
	require.EqualValues(t, 1, old.Load())
}

func TestHubReplicaReleaseNeverWaitsForBackendCleanup(t *testing.T) {
	h := NewHub()
	stream := newLifecycleStream()
	h.SetStream(stream)
	var held atomic.Int32
	h.setReplicaCleanupAdmission(func() (func(), bool) {
		if !held.CompareAndSwap(0, 1) {
			return nil, false
		}
		return func() { held.Add(-1) }, true
	})
	started, unblock := make(chan struct{}), make(chan struct{})
	stream.unsubscribe = func(string) error { close(started); <-unblock; return nil }
	first, err := h.SubscribeReplica(context.Background(), "db", "users", func() {}, func(error) {})
	require.NoError(t, err)
	second, err := h.SubscribeReplica(context.Background(), "db", "users", func() {}, func(error) {})
	require.NoError(t, err)
	released := make(chan struct{})
	go func() { first(); close(released) }()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("release waited for backend")
	}
	<-started
	require.EqualValues(t, 1, held.Load())
	second()
	require.Eventually(t, func() bool { return stream.closed.Load() == 1 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, held.Load(), "capacity remains owned by the blocked worker")
	close(unblock)
	require.Eventually(t, func() bool { return held.Load() == 0 }, time.Second, time.Millisecond)
	first()
	second()
	require.EqualValues(t, 1, stream.closed.Load())
}

func TestHubReplicaCallbacksAndShutdownAreOutsideHubLocks(t *testing.T) {
	h := NewHub()
	stream := newLifecycleStream()
	h.SetStream(stream)
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	var changed, invalidated, closed atomic.Int32
	release, err := h.SubscribeReplica(ctx, "db", "users", func() {
		h.replicaMu.Lock()
		h.replicaMu.Unlock()
		changed.Add(1)
	}, func(error) { h.replicaMu.Lock(); h.replicaMu.Unlock(); invalidated.Add(1) })
	require.NoError(t, err)
	_, ok := h.RegisterReplicaConnection(func() { h.replicaMu.Lock(); h.replicaMu.Unlock(); closed.Add(1) })
	require.True(t, ok)
	h.replicaMu.Lock()
	var id string
	for key := range h.replicaSubscriptions {
		id = key
	}
	h.replicaMu.Unlock()
	h.deliverReplica(&streamer.EventDelivery{SubscriptionIDs: []string{id}})
	require.EqualValues(t, 1, changed.Load())
	cancel()
	require.Eventually(t, func() bool { return closed.Load() == 1 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, invalidated.Load())
	release()
	h.deliverReplica(&streamer.EventDelivery{SubscriptionIDs: []string{id}})
	require.EqualValues(t, 1, changed.Load())
}

func TestHubStreamReplacementClosesOrdinaryClientsOnce(t *testing.T) {
	h := NewHub()
	h.SetStream(newLifecycleStream())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)
	client := &Client{send: make(chan BaseMessage, 1)}
	require.True(t, h.Register(client))
	require.Eventually(t, func() bool { h.mu.RLock(); defer h.mu.RUnlock(); return h.clients[client] }, time.Second, time.Millisecond)
	h.SetStream(newLifecycleStream())
	select {
	case _, open := <-client.send:
		require.False(t, open)
	case <-time.After(time.Second):
		t.Fatal("ordinary client stayed open")
	}
	h.Unregister(client)
	h.SetStream(nil)
}

func TestHubLegacyRegistrationLeaseRejectsReplacementAndGenerationChange(t *testing.T) {
	h := NewHub()
	stream := newLifecycleStream()
	h.SetStream(stream)
	registration, err := h.SubscribeToStream("db", "users", nil)
	require.NoError(t, err)
	stream.transition(streamer.StateConnected, 2)
	require.False(t, h.RegisterSubscription(registration, &Client{}, "old-generation"))
	registration, err = h.SubscribeToStream("db", "users", nil)
	require.NoError(t, err)
	h.SetStream(newLifecycleStream())
	require.False(t, h.RegisterSubscription(registration, &Client{}, "old-stream"))
}

func TestHubReplicaWakesDoNotWaitForFullLegacyQueue(t *testing.T) {
	h := NewHub()
	stream := newLifecycleStream()
	h.SetStream(stream)
	var wakes atomic.Int32
	release, err := h.SubscribeReplica(context.Background(), "db", "users", func() { wakes.Add(1) }, func(error) {})
	require.NoError(t, err)
	defer release()
	h.replicaMu.Lock()
	var replicaID string
	for id := range h.replicaSubscriptions {
		replicaID = id
	}
	h.replicaMu.Unlock()
	legacy, err := h.SubscribeToStream("db", "users", nil)
	require.NoError(t, err)
	client := &Client{send: make(chan BaseMessage, 1), subscriptions: map[string]Subscription{"legacy": {}}}
	require.True(t, h.RegisterSubscription(legacy, client, "legacy"))
	delivery := &streamer.EventDelivery{SubscriptionIDs: []string{replicaID, legacy.ID}, Event: &streamer.Event{Operation: streamer.OperationUpdate}}
	for i := 0; i < 100; i++ {
		h.BroadcastStreamDelivery(stream, delivery)
	}
	require.EqualValues(t, 100, wakes.Load())
	require.Equal(t, cap(h.broadcast), len(h.broadcast))
	queued := <-h.broadcast
	replacement := newLifecycleStream()
	h.SetStream(replacement)
	// Even an identical registration ID cannot inherit an old queued event.
	replacement.subscribe = func(context.Context) (streamer.Registration, error) {
		return streamer.Registration{ID: legacy.ID, Generation: 1}, nil
	}
	fresh, err := h.SubscribeToStream("db", "users", nil)
	require.NoError(t, err)
	freshClient := &Client{send: make(chan BaseMessage, 1), subscriptions: map[string]Subscription{"fresh": {}}}
	require.True(t, h.RegisterSubscription(fresh, freshClient, "fresh"))
	h.deliverLegacy(queued)
	require.Empty(t, freshClient.send)
	h.BroadcastStreamDelivery(stream, delivery)
	require.EqualValues(t, 100, wakes.Load())
}

func TestHubLegacyCleanupUsesItsLeaseWhenReplacementReusesID(t *testing.T) {
	h := NewHub()
	oldStream, newStream := newLifecycleStream(), newLifecycleStream()
	var oldUnsubscribed, newUnsubscribed atomic.Int32
	oldStream.unsubscribe = func(id string) error { require.Equal(t, "same-id", id); oldUnsubscribed.Add(1); return nil }
	newStream.unsubscribe = func(id string) error { require.Equal(t, "same-id", id); newUnsubscribed.Add(1); return nil }
	for _, stream := range []*lifecycleStream{oldStream, newStream} {
		stream.subscribe = func(context.Context) (streamer.Registration, error) {
			return streamer.Registration{ID: "same-id", Generation: 1}, nil
		}
	}
	h.SetStream(oldStream)
	oldRegistration, err := h.SubscribeToStream("db", "users", nil)
	require.NoError(t, err)
	require.True(t, h.RegisterSubscription(oldRegistration, &Client{}, "old"))
	h.SetStream(newStream)
	newRegistration, err := h.SubscribeToStream("db", "users", nil)
	require.NoError(t, err)
	newClient := &Client{send: make(chan BaseMessage, 1), subscriptions: map[string]Subscription{"new": {}}}
	require.True(t, h.RegisterSubscription(newRegistration, newClient, "new"))
	h.ReleaseSubscription(oldRegistration)
	h.ReleaseSubscription(oldRegistration)
	require.Eventually(t, func() bool { return oldUnsubscribed.Load() == 1 }, time.Second, time.Millisecond)
	require.Zero(t, newUnsubscribed.Load())
	h.subscriptionsMu.RLock()
	info := h.subscriptions["same-id"]
	h.subscriptionsMu.RUnlock()
	require.NotNil(t, info)
	require.Same(t, newClient, info.Client)
	h.deliverLegacy(hubDelivery{owner: newRegistration.owner, delivery: &streamer.EventDelivery{
		SubscriptionIDs: []string{"same-id"}, Event: &streamer.Event{Operation: streamer.OperationUpdate},
	}})
	require.Len(t, newClient.send, 1)
	h.ReleaseSubscription(newRegistration)
	require.Eventually(t, func() bool { return newUnsubscribed.Load() == 1 }, time.Second, time.Millisecond)
}

func TestLegacyOverflowRetirementDoesNotFollowReusedSubscriptionIDs(t *testing.T) {
	hub := NewHub()
	oldStream, newStream := newLifecycleStream(), newLifecycleStream()
	for _, stream := range []*lifecycleStream{oldStream, newStream} {
		stream.subscribe = func(context.Context) (streamer.Registration, error) {
			return streamer.Registration{ID: "reused", Generation: 1}, nil
		}
	}
	hub.SetStream(oldStream)
	oldRegistration, err := hub.SubscribeToStream("db", "users", nil)
	require.NoError(t, err)
	oldClient := &Client{hub: hub, send: make(chan BaseMessage, 256), subscriptions: map[string]Subscription{"old": {}}}
	hub.clients[oldClient] = true
	require.True(t, hub.RegisterSubscription(oldRegistration, oldClient, "old"))
	captured := map[*Client]struct{}{oldClient: {}}
	hub.SetStream(newStream)
	newRegistration, err := hub.SubscribeToStream("db", "users", nil)
	require.NoError(t, err)
	newClient := &Client{hub: hub, send: make(chan BaseMessage, 256), subscriptions: map[string]Subscription{"new": {}}}
	hub.clients[newClient] = true
	t.Cleanup(newClient.closeOutbound)
	t.Cleanup(func() { hub.ReleaseSubscription(newRegistration); hub.ReleaseSubscription(oldRegistration) })
	require.True(t, hub.RegisterSubscription(newRegistration, newClient, "new"))
	hub.retireLegacyOverflow(oldRegistration.owner, captured)
	select {
	case <-newClient.outboundDone():
		t.Fatal("stale overflow retired a replacement client")
	default:
	}
	require.True(t, hub.clients[newClient])
	hub.deliverLegacy(hubDelivery{owner: newRegistration.owner, delivery: &streamer.EventDelivery{
		SubscriptionIDs: []string{newRegistration.ID}, Event: &streamer.Event{Operation: streamer.OperationUpdate},
	}})
	require.Len(t, newClient.send, 1)
}

type closeFailureStream struct {
	*lifecycleStream
	closeReturned chan struct{}
}

func (s *closeFailureStream) Close() error {
	s.closed.Add(1)
	close(s.closeReturned)
	return errors.New("backend close failed")
}

func TestHubCleanupRetirementRemainsClosedWhenBackendCloseFails(t *testing.T) {
	h := NewHub()
	s := &closeFailureStream{lifecycleStream: newLifecycleStream(), closeReturned: make(chan struct{})}
	h.SetStream(s)
	h.setReplicaCleanupAdmission(func() (func(), bool) { return nil, false })
	var closed atomic.Int32
	_, ok := h.RegisterReplicaConnection(func() { closed.Add(1) })
	require.True(t, ok)
	release, err := h.SubscribeReplica(context.Background(), "db", "users", func() {}, func(error) {})
	require.NoError(t, err)
	release()
	select {
	case <-s.closeReturned:
	case <-time.After(time.Second):
		t.Fatal("retirement did not close its actual backend")
	}
	require.EqualValues(t, 1, closed.Load())
	_, ok = h.RegisterReplicaConnection(func() { t.Error("retired stream accepted a new connection") })
	require.False(t, ok)
	h.retireReplicaStream(h.streamOwner, errors.New("second cleanup failure"))
	require.EqualValues(t, 1, s.closed.Load())
}

type checkedACKStream struct {
	*lifecycleStream
	checks     atomic.Int32
	ackChecked chan struct{}
}

func (s *checkedACKStream) Status() streamer.StreamStatus {
	status := s.lifecycleStream.Status()
	if s.checks.Add(1) == 2 {
		close(s.ackChecked)
	}
	return status
}

func TestLegacyClientRejectsACKOwnerReplacedBeforeRegistration(t *testing.T) {
	h := NewHub()
	s := &checkedACKStream{lifecycleStream: newLifecycleStream(), ackChecked: make(chan struct{})}
	var cleaned atomic.Int32
	s.unsubscribe = func(string) error { cleaned.Add(1); return nil }
	h.SetStream(s)
	c := &Client{hub: h, authenticated: true, database: "db", send: make(chan BaseMessage, 1),
		subscriptions: make(map[string]Subscription), streamerSubIDs: make(map[string]hubRegistration)}
	c.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.handleMessage(BaseMessage{ID: "subscribe", Type: TypeSubscribe, Payload: mustMarshal(SubscribePayload{Query: model.Query{Collection: "users"}})})
	}()
	select {
	case <-s.ackChecked:
	case <-time.After(time.Second):
		c.mu.Unlock()
		t.Fatal("subscription never reached its ACK ownership check")
	}
	h.SetStream(newLifecycleStream())
	c.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("obsolete ACK did not terminate")
	}
	message := <-c.send
	require.Equal(t, TypeError, message.Type)
	require.Contains(t, string(message.Payload), "unavailable")
	require.Empty(t, c.subscriptions)
	require.Empty(t, c.streamerSubIDs)
	require.Eventually(t, func() bool { return cleaned.Load() == 1 }, time.Second, time.Millisecond)
}
