package streamer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/puller"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/stretchr/testify/require"
)

type admissionPuller struct {
	ch     chan *puller.Event
	status *puller.SubscriptionStatus
}

func (p *admissionPuller) Subscribe(context.Context, string, string) <-chan *puller.Event {
	return p.ch
}
func (p *admissionPuller) SubscribeStatus(context.Context, string, string) (<-chan *puller.Event, *puller.SubscriptionStatus) {
	return p.ch, p.status
}

func TestStreamerReadinessRequiresConsumedAdmissionAndTracksTerminalCause(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &admissionPuller{ch: make(chan *puller.Event, 2), status: events.NewSubscriptionStatus(ctx)}
	transport := source.status.BeginAttempt()
	service, err := NewService(ServerConfig{}, nil, WithPullerClient(source))
	require.NoError(t, err)
	s := service.(*streamerService)
	require.Error(t, s.Readiness(ctx))
	require.NoError(t, s.TerminalError())
	defer s.Stop(ctx)
	require.NoError(t, s.Start(ctx))
	require.ErrorContains(t, s.Start(ctx), "already started")
	require.Error(t, s.Readiness(ctx))
	source.ch <- &puller.Event{Change: &events.StoreChangeEvent{EventID: "one", OpType: events.StoreOperationInsert}, Progress: "prefix"}
	ready := &puller.Event{Ready: true, AdmissionState: events.AdmissionAdmitted, AdmissionGeneration: 1, TransportGeneration: transport}
	require.True(t, source.status.Receive(ready))
	source.ch <- ready
	require.Eventually(t, func() bool { return s.Readiness(ctx) == nil }, time.Second, time.Millisecond)
	recovery := &puller.Event{AdmissionState: events.AdmissionRecovering, AdmissionGeneration: 2, TransportGeneration: transport}
	require.True(t, source.status.Receive(recovery))
	source.ch <- recovery
	require.Error(t, s.Readiness(ctx))
	source.status.Observe(ready)
	require.Error(t, s.Readiness(ctx))
	cause := errors.New("source failure")
	source.status.Fail(cause, true)
	require.ErrorIs(t, s.TerminalError(), cause)
	close(source.ch)
	require.Eventually(t, func() bool { return s.TerminalError() != nil }, time.Second, time.Millisecond)
	require.ErrorIs(t, s.Readiness(ctx), cause)
}

func TestStreamerStopClearsReadinessAndCannotRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source := &admissionPuller{ch: make(chan *puller.Event, 1), status: events.NewSubscriptionStatus(ctx)}
	transport := source.status.BeginAttempt()
	service, err := NewService(ServerConfig{}, nil, WithPullerClient(source))
	require.NoError(t, err)
	s := service.(*streamerService)
	require.NoError(t, s.Start(ctx))
	ready := &puller.Event{Ready: true, AdmissionState: events.AdmissionAdmitted, AdmissionGeneration: 1, TransportGeneration: transport}
	require.True(t, source.status.Receive(ready))
	source.ch <- ready
	require.Eventually(t, func() bool { return s.Readiness(ctx) == nil }, time.Second, time.Millisecond)
	require.NoError(t, s.Stop(ctx))
	require.ErrorIs(t, s.Readiness(ctx), context.Canceled)
	require.Error(t, s.Start(ctx))
	require.NoError(t, s.TerminalError())
}

func TestStreamerRejectsUnobservablePuller(t *testing.T) {
	s, err := NewService(ServerConfig{}, nil, WithPullerClient(subscribeOnly{}))
	require.NoError(t, err)
	defer s.Stop(context.Background())
	require.ErrorContains(t, s.Start(context.Background()), "admission unavailable")
}

type subscribeOnly struct{}

func (subscribeOnly) Subscribe(context.Context, string, string) <-chan *puller.Event { return nil }

func TestStreamerPublishesMalformedSourceFailuresFromOwnedLoop(t *testing.T) {
	for _, kind := range []string{"nil", "unknown", "failure"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			source := &admissionPuller{ch: make(chan *puller.Event, 1), status: events.NewSubscriptionStatus(ctx)}
			service, err := NewService(ServerConfig{}, nil, WithPullerClient(source))
			require.NoError(t, err)
			s := service.(*streamerService)
			defer s.Stop(ctx)
			require.NoError(t, s.Start(ctx))
			switch kind {
			case "nil":
				source.ch <- nil
			case "unknown":
				source.ch <- &puller.Event{Change: &events.StoreChangeEvent{OpType: "unknown"}}
			case "failure":
				source.ch <- &puller.Event{Error: errors.New("source failed")}
			}
			require.Eventually(t, func() bool { return s.TerminalError() != nil }, time.Second, time.Millisecond)
			require.Error(t, s.Readiness(ctx))
		})
	}
}

func TestStreamerStopSettlesBlockedFanoutWithoutTerminalFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source := &admissionPuller{ch: make(chan *puller.Event, 1), status: events.NewSubscriptionStatus(ctx)}
	service, err := NewService(ServerConfig{SendTimeout: time.Hour}, nil, WithPullerClient(source))
	require.NoError(t, err)
	s := service.(*streamerService)
	require.NoError(t, s.Start(ctx))
	stream, err := s.Stream(ctx)
	require.NoError(t, err)
	_, err = stream.Subscribe(ctx, "db", "users", nil)
	require.NoError(t, err)
	local := stream.(*localStream)
	for n := 0; n < cap(local.outgoing); n++ {
		local.outgoing <- &EventDelivery{}
	}
	source.ch <- &puller.Event{Change: &events.StoreChangeEvent{OpType: events.StoreOperationInsert, Database: "db", FullDocument: testStoredDoc("users", "one", "db", nil)}, Progress: "prefix"}
	require.Eventually(t, func() bool {
		if s.streamsMu.TryLock() {
			s.streamsMu.Unlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond)
	require.NoError(t, s.Stop(ctx))
	require.NoError(t, s.TerminalError())
	require.Error(t, s.Readiness(ctx))
}
