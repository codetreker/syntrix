package watcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type statusPuller struct {
	ch     chan *events.PullerEvent
	status *events.SubscriptionStatus
}

func (p *statusPuller) Subscribe(context.Context, string, string) <-chan *events.PullerEvent {
	return p.ch
}
func (p *statusPuller) SubscribeStatus(context.Context, string, string) (<-chan *events.PullerEvent, *events.SubscriptionStatus) {
	return p.ch, p.status
}

func TestWatcherAdmissionWaitsForEvaluatorAcknowledgmentAndRejectsRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &statusPuller{ch: make(chan *events.PullerEvent, 3), status: events.NewSubscriptionStatus(ctx)}
	transport := source.status.BeginAttempt()
	store := new(MockDocumentStore)
	store.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Return(nil, model.ErrNotFound)
	watcher := NewWatcher(source, store, WatcherOptions{StartFromNow: true}).(*pullerWatcher)
	require.Error(t, watcher.Readiness(ctx))
	require.NoError(t, watcher.TerminalError())
	defer watcher.Close()
	out, err := watcher.Watch(ctx)
	require.NoError(t, err)
	_, err = watcher.Watch(ctx)
	require.ErrorContains(t, err, "already started")
	source.ch <- &events.PullerEvent{Change: &events.StoreChangeEvent{EventID: "event", OpType: events.StoreOperationInsert}, Progress: "prefix"}
	ready := &events.PullerEvent{Ready: true, AdmissionState: events.AdmissionAdmitted, AdmissionGeneration: 1, TransportGeneration: transport}
	require.True(t, source.status.Receive(ready))
	source.ch <- ready
	evt := <-out
	require.NotZero(t, evt.AdmissionSequence)
	require.Error(t, watcher.Readiness(ctx), "receiving an event does not prove evaluation completed")
	watcher.Acknowledge(evt.AdmissionSequence)
	require.Eventually(t, func() bool { return watcher.Readiness(ctx) == nil }, time.Second, time.Millisecond)
	recovery := &events.PullerEvent{AdmissionState: events.AdmissionRecovering, AdmissionGeneration: 2, TransportGeneration: transport}
	require.True(t, source.status.Receive(recovery))
	source.ch <- recovery
	require.Error(t, watcher.Readiness(ctx))
	source.status.Observe(ready)
	require.Error(t, watcher.Readiness(ctx))
	cause := errors.New("source failure")
	source.status.Fail(cause, true)
	require.ErrorIs(t, watcher.TerminalError(), cause)
	close(source.ch)
	require.Eventually(t, func() bool { watcher.mu.RLock(); defer watcher.mu.RUnlock(); return watcher.terminal != nil }, time.Second, time.Millisecond)
	require.ErrorIs(t, watcher.Readiness(ctx), cause)
}

func TestWatcherAcknowledgmentDoesNotCertifyFollowingDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &statusPuller{ch: make(chan *events.PullerEvent, 3), status: events.NewSubscriptionStatus(ctx)}
	transport := source.status.BeginAttempt()
	store := new(MockDocumentStore)
	store.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Return(nil, model.ErrNotFound)
	watcher := NewWatcher(source, store, WatcherOptions{StartFromNow: true}).(*pullerWatcher)
	defer watcher.Close()
	out, err := watcher.Watch(ctx)
	require.NoError(t, err)
	for _, id := range []string{"one", "two"} {
		source.ch <- &events.PullerEvent{Change: &events.StoreChangeEvent{EventID: id, OpType: events.StoreOperationInsert}, Progress: "repeated-token"}
	}
	ready := &events.PullerEvent{Ready: true, AdmissionState: events.AdmissionAdmitted, AdmissionGeneration: 1, TransportGeneration: transport}
	require.True(t, source.status.Receive(ready))
	source.ch <- ready
	first := <-out
	second := <-out
	watcher.Acknowledge(first.AdmissionSequence)
	require.Error(t, watcher.Readiness(ctx))
	watcher.Acknowledge(second.AdmissionSequence)
	require.Eventually(t, func() bool { return watcher.Readiness(ctx) == nil }, time.Second, time.Millisecond)
	require.NoError(t, watcher.Close())
	require.ErrorIs(t, watcher.Readiness(ctx), context.Canceled)
}

func TestWatcherRejectsMalformedDeliveryAndUnobservableSource(t *testing.T) {
	for _, kind := range []string{"nil", "unknown", "failure", "unsupported", "empty"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			source := &statusPuller{ch: make(chan *events.PullerEvent, 1), status: events.NewSubscriptionStatus(ctx)}
			store := new(MockDocumentStore)
			var input interface {
				Subscribe(context.Context, string, string) <-chan *events.PullerEvent
			} = source
			if kind == "unsupported" {
				input = ordinarySource{}
			}
			if kind == "empty" {
				source.ch = nil
			}
			watcher := NewWatcher(input, store, WatcherOptions{StartFromNow: true}).(*pullerWatcher)
			defer watcher.Close()
			if kind != "unsupported" {
				store.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Return(nil, model.ErrNotFound)
			}
			out, err := watcher.Watch(ctx)
			if kind == "unsupported" || kind == "empty" {
				require.Error(t, err)
				return
			}
			switch kind {
			case "nil":
				source.ch <- nil
			case "unknown":
				source.ch <- &events.PullerEvent{Change: &events.StoreChangeEvent{OpType: "unknown"}}
			case "failure":
				source.ch <- &events.PullerEvent{Error: errors.New("failed source")}
			}
			select {
			case _, open := <-out:
				require.False(t, open)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.Error(t, watcher.TerminalError())
			require.Error(t, watcher.Readiness(ctx))
		})
	}
}

type ordinarySource struct{}

func (ordinarySource) Subscribe(context.Context, string, string) <-chan *events.PullerEvent {
	return nil
}

func TestWatcherShutdownCancelsCheckpointStartupWithoutBlockingReadiness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source := &statusPuller{ch: make(chan *events.PullerEvent), status: events.NewSubscriptionStatus(ctx)}
	store := new(MockDocumentStore)
	entered := make(chan struct{})
	store.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Run(func(args mock.Arguments) { close(entered); <-args.Get(0).(context.Context).Done() }).Return(nil, context.Canceled)
	watcher := NewWatcher(source, store, WatcherOptions{StartFromNow: true}).(*pullerWatcher)
	watchDone := make(chan error, 1)
	go func() { _, err := watcher.Watch(ctx); watchDone <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Error(t, watcher.Readiness(ctx))
	require.NoError(t, watcher.Shutdown(ctx))
	select {
	case err := <-watchDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestWatcherShutdownCancelsBlockedBusinessOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source := &statusPuller{ch: make(chan *events.PullerEvent, 1), status: events.NewSubscriptionStatus(ctx)}
	store := new(MockDocumentStore)
	store.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Return(nil, model.ErrNotFound)
	watcher := NewWatcher(source, store, WatcherOptions{StartFromNow: true}).(*pullerWatcher)
	out, err := watcher.Watch(ctx)
	require.NoError(t, err)
	source.ch <- &events.PullerEvent{Change: &events.StoreChangeEvent{OpType: events.StoreOperationInsert}, Progress: "prefix"}
	require.Eventually(t, func() bool { watcher.mu.RLock(); defer watcher.mu.RUnlock(); return watcher.delivered == 1 }, time.Second, time.Millisecond)
	require.NoError(t, watcher.Shutdown(ctx))
	_, open := <-out
	require.False(t, open)
}

func TestWatcherShutdownDeadlineDoesNotClaimUnsettledStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source := &statusPuller{ch: make(chan *events.PullerEvent), status: events.NewSubscriptionStatus(ctx)}
	store := new(MockDocumentStore)
	entered, release := make(chan struct{}), make(chan struct{})
	store.On("Get", mock.Anything, "default", "sys/checkpoints/trigger_evaluator").Run(func(args mock.Arguments) { close(entered); <-args.Get(0).(context.Context).Done(); <-release }).Return(nil, context.Canceled)
	watcher := NewWatcher(source, store, WatcherOptions{StartFromNow: true}).(*pullerWatcher)
	go func() { _, _ = watcher.Watch(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	deadline, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	require.ErrorIs(t, watcher.Shutdown(deadline), context.DeadlineExceeded)
	select {
	case <-watcher.done:
		t.Fatal("startup falsely reported settlement")
	default:
	}
	close(release)
	require.NoError(t, watcher.Shutdown(ctx))
}
