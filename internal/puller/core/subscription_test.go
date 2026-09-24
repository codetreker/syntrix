package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

type subscriptionIterator struct {
	events   []*events.StoreChangeEvent
	index    int
	nextHook func(int)
	err      error
	closeErr error
	closed   int
}

func newSubscriptionIterator(events ...*events.StoreChangeEvent) *subscriptionIterator {
	return &subscriptionIterator{events: events, index: -1}
}

func (i *subscriptionIterator) Next() bool {
	i.index++
	if i.nextHook != nil {
		i.nextHook(i.index + 1)
	}
	return i.index < len(i.events)
}

func (i *subscriptionIterator) Event() *events.StoreChangeEvent {
	return i.events[i.index]
}

func (i *subscriptionIterator) Err() error {
	return i.err
}

func (i *subscriptionIterator) Close() error {
	i.closed++
	return i.closeErr
}

func subscriptionEvent(index int) *events.StoreChangeEvent {
	return &events.StoreChangeEvent{
		Backend:     "source",
		EventID:     time.Date(2026, 1, 1, 0, 0, index, 0, time.UTC).Format("05") + "-1-event",
		ClusterTime: events.ClusterTime{T: uint32(index), I: 1},
	}
}

func TestRunSubscriptionRecoveryReplaysBehindFenceAndAnnouncesReadyOnce(t *testing.T) {
	sub := testSubscriber(t, "runner", cursor.NewProgressMarker(), false, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := subscriptionEvent(1)
	live := subscriptionEvent(2)
	queued := subscriptionEvent(3)
	dropped := subscriptionEvent(4)
	initialIter := newSubscriptionIterator(initial)
	recoveryIter := newSubscriptionIterator(initial, live, queued, dropped)
	iterators := []*subscriptionIterator{initialIter, recoveryIter}
	var mu sync.Mutex
	var delivered []string
	var replayProgress []string
	var replayQueueLengths []int
	readyCount := 0
	enterLiveCount := 0
	ready := make(chan struct{})
	liveDelivery := make(chan struct{})
	releaseLive := make(chan struct{})
	deliveredAll := make(chan struct{})
	driver := SubscriptionDriver{
		OpenReplay: func(_ context.Context, progress *cursor.ProgressMarker) (events.Iterator, error) {
			mu.Lock()
			defer mu.Unlock()
			replayProgress = append(replayProgress, progress.Encode())
			replayQueueLengths = append(replayQueueLengths, len(sub.Events()))
			iter := iterators[0]
			iterators = iterators[1:]
			return iter, nil
		},
		Deliver: func(ctx context.Context, evt *events.StoreChangeEvent, _ string) error {
			if evt == live {
				close(liveDelivery)
				select {
				case <-releaseLive:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			mu.Lock()
			delivered = append(delivered, evt.EventID)
			if len(delivered) == 4 {
				close(deliveredAll)
			}
			mu.Unlock()
			return nil
		},
		Ready: func(context.Context, string) error {
			mu.Lock()
			readyCount++
			mu.Unlock()
			close(ready)
			return nil
		},
		EnterLive: func() {
			mu.Lock()
			enterLiveCount++
			mu.Unlock()
		},
	}
	exitCh := make(chan SubscriptionExit, 1)
	go func() {
		exitCh <- RunSubscription(ctx, sub, true, driver)
	}()

	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("subscription did not announce readiness")
	}
	admitted, _ := sub.enqueue(live)
	require.True(t, admitted)
	select {
	case <-liveDelivery:
	case <-time.After(time.Second):
		t.Fatal("subscription did not start live delivery")
	}
	admitted, _ = sub.enqueue(queued)
	require.True(t, admitted)
	admitted, recoveryStarted := sub.enqueue(dropped)
	require.False(t, admitted)
	require.True(t, recoveryStarted)
	close(releaseLive)

	select {
	case <-deliveredAll:
	case <-time.After(time.Second):
		t.Fatal("subscription did not complete retained recovery")
	}
	cancel()
	exit := <-exitCh
	require.Equal(t, SubscriptionExitContextCanceled, exit.Kind)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{initial.EventID, live.EventID, queued.EventID, dropped.EventID}, delivered)
	require.Len(t, replayProgress, 2)
	require.Empty(t, replayProgress[0])
	secondProgress, err := cursor.DecodeProgressMarker(replayProgress[1])
	require.NoError(t, err)
	require.Equal(t, live.EventID, secondProgress.Positions["source"])
	require.Equal(t, []int{0, 0}, replayQueueLengths)
	require.Equal(t, 1, readyCount)
	require.Equal(t, 2, enterLiveCount)
	require.Equal(t, 1, initialIter.closed)
	require.Equal(t, 1, recoveryIter.closed)
}

func TestRunSubscriptionStartFromNowFiltersHistoryBeforeCoalescing(t *testing.T) {
	sub, err := NewSubscriber("current-head", cursor.NewProgressMarker(), true, true, 1)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	event := func(backend, id, doc string, second uint32, operation events.StoreOperationType) *events.StoreChangeEvent {
		return &events.StoreChangeEvent{
			Backend: backend, EventID: id, MgoColl: "items", MgoDocID: doc,
			ClusterTime: events.ClusterTime{T: second, I: 1}, OpType: operation,
		}
	}
	old := event("a", "1-1-old", "shared", 1, events.StoreOperationInsert)
	oldSibling := event("a", "2-1-old", "prior", 2, events.StoreOperationUpdate)
	first := event("a", "2-1-first", "shared", 2, events.StoreOperationDelete)
	second := event("a", "2-1-second", "other", 2, events.StoreOperationUpdate)
	quietOld := event("b", "1-1-old", "quiet", 1, events.StoreOperationInsert)
	quietNew := event("b", "3-1-new", "quiet", 3, events.StoreOperationUpdate)

	admitted, _ := sub.enqueue(first)
	require.True(t, admitted)
	admitted, started := sub.enqueue(second)
	require.False(t, admitted)
	require.True(t, started)
	admitted, _ = sub.enqueue(quietNew)
	require.False(t, admitted)

	var delivered []string
	exit := RunSubscription(ctx, sub, false, SubscriptionDriver{
		OpenAdmissionReplay: func(_ context.Context, progress *cursor.ProgressMarker, floors map[string]events.ClusterTime) (events.Iterator, error) {
			require.Empty(t, progress.Positions)
			require.Equal(t, map[string]events.ClusterTime{
				"a": first.ClusterTime,
				"b": quietNew.ClusterTime,
			}, floors)
			return newSubscriptionIterator(old, oldSibling, first, second, quietOld, quietNew), nil
		},
		Deliver: func(_ context.Context, evt *events.StoreChangeEvent, _ string) error {
			delivered = append(delivered, evt.EventID)
			if len(delivered) == 3 {
				cancel()
			}
			return nil
		},
	})
	require.Equal(t, SubscriptionExitContextCanceled, exit.Kind)
	require.ElementsMatch(t, []string{first.EventID, second.EventID, quietNew.EventID}, delivered)
}

func TestRunSubscriptionStartFromNowStopsDuringFilteredReplay(t *testing.T) {
	sub, err := NewSubscriber("cancel-filter", cursor.NewProgressMarker(), true, false, 1)
	require.NoError(t, err)
	post := subscriptionEvent(2)
	sub.enqueue(post)
	sub.enqueue(subscriptionEvent(3))
	require.True(t, sub.RecoveryPending())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := subscriptionEvent(1)
	iter := newSubscriptionIterator(old, old, old)
	iter.nextHook = func(count int) {
		if count == 1 {
			cancel()
		}
	}
	exit := RunSubscription(ctx, sub, false, SubscriptionDriver{
		OpenAdmissionReplay: func(context.Context, *cursor.ProgressMarker, map[string]events.ClusterTime) (events.Iterator, error) {
			return iter, nil
		},
		Deliver: func(context.Context, *events.StoreChangeEvent, string) error {
			t.Fatal("canceled replay delivered an event")
			return nil
		},
	})
	require.Equal(t, SubscriptionExitContextCanceled, exit.Kind)
	require.Equal(t, 1, iter.index+1)
	require.Equal(t, 1, iter.closed)
}

func TestRunSubscriptionSeparatesDeliveryAndIteratorCleanupFailures(t *testing.T) {
	deliveryErr := errors.New("delivery failed")
	closeErr := errors.New("close failed")
	iter := newSubscriptionIterator(subscriptionEvent(1))
	iter.closeErr = closeErr
	sub := testSubscriber(t, "runner", nil, false, 1)

	exit := RunSubscription(context.Background(), sub, true, SubscriptionDriver{
		OpenReplay: func(context.Context, *cursor.ProgressMarker) (events.Iterator, error) {
			return iter, nil
		},
		Deliver: func(context.Context, *events.StoreChangeEvent, string) error {
			return deliveryErr
		},
	})

	require.Equal(t, SubscriptionExitDeliveryFailed, exit.Kind)
	require.Equal(t, SubscriptionPhaseReplay, exit.Phase)
	require.ErrorIs(t, exit.Err, deliveryErr)
	require.ErrorIs(t, exit.CleanupErr, closeErr)
	require.Equal(t, 1, iter.closed)
	require.Empty(t, sub.CurrentProgress().Positions)
}

func TestRunSubscriptionPreservesReplayAndCleanupFailures(t *testing.T) {
	openErr := errors.New("open failed")
	iterationErr := errors.New("iteration failed")
	closeErr := errors.New("close failed")
	for _, tc := range []struct {
		name        string
		open        func() (events.Iterator, error)
		wantErr     error
		wantCleanup error
	}{
		{
			name: "open",
			open: func() (events.Iterator, error) {
				return nil, openErr
			},
			wantErr: openErr,
		},
		{
			name: "iteration and cleanup",
			open: func() (events.Iterator, error) {
				return &subscriptionIterator{index: -1, err: iterationErr, closeErr: closeErr}, nil
			},
			wantErr:     iterationErr,
			wantCleanup: closeErr,
		},
		{
			name: "cleanup",
			open: func() (events.Iterator, error) {
				return &subscriptionIterator{index: -1, closeErr: closeErr}, nil
			},
			wantCleanup: closeErr,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := testSubscriber(t, "runner", nil, false, 1)
			exit := RunSubscription(context.Background(), sub, true, SubscriptionDriver{
				OpenReplay: func(context.Context, *cursor.ProgressMarker) (events.Iterator, error) {
					return tc.open()
				},
				Deliver: func(context.Context, *events.StoreChangeEvent, string) error {
					return nil
				},
			})
			require.Equal(t, SubscriptionExitReplayFailed, exit.Kind)
			require.Equal(t, SubscriptionPhaseReplay, exit.Phase)
			if tc.wantErr == nil {
				require.NoError(t, exit.Err)
			} else {
				require.ErrorIs(t, exit.Err, tc.wantErr)
			}
			if tc.wantCleanup == nil {
				require.NoError(t, exit.CleanupErr)
			} else {
				require.ErrorIs(t, exit.CleanupErr, tc.wantCleanup)
			}
		})
	}
}

func TestRunSubscriptionDistinguishesCancellationAndSubscriberClosure(t *testing.T) {
	t.Run("context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		exit := RunSubscription(ctx, testSubscriber(t, "runner", nil, false, 1), false, SubscriptionDriver{})
		require.Equal(t, SubscriptionExitContextCanceled, exit.Kind)
		require.ErrorIs(t, exit.Err, context.Canceled)
	})

	t.Run("subscriber", func(t *testing.T) {
		sub := testSubscriber(t, "runner", nil, false, 1)
		sub.Close()
		exit := RunSubscription(context.Background(), sub, false, SubscriptionDriver{})
		require.Equal(t, SubscriptionExitSubscriberClosed, exit.Kind)
		require.NoError(t, exit.Err)
	})
}

func TestRunSubscriptionStopsBeforeInitialOrRecoveryReplay(t *testing.T) {
	for _, tc := range []struct {
		name           string
		initialCatchUp bool
		prepare        func(context.CancelFunc, *Subscriber)
		wantKind       SubscriptionExitKind
		wantQueued     int
		wantPending    bool
	}{
		{
			name:           "pre-canceled initial replay",
			initialCatchUp: true,
			prepare:        func(cancel context.CancelFunc, _ *Subscriber) { cancel() },
			wantKind:       SubscriptionExitContextCanceled,
		},
		{
			name:           "pre-closed initial replay",
			initialCatchUp: true,
			prepare:        func(_ context.CancelFunc, sub *Subscriber) { sub.Close() },
			wantKind:       SubscriptionExitSubscriberClosed,
		},
		{
			name: "canceled recovery",
			prepare: func(cancel context.CancelFunc, sub *Subscriber) {
				queueRecovery(t, sub)
				cancel()
			},
			wantKind:    SubscriptionExitContextCanceled,
			wantQueued:  1,
			wantPending: true,
		},
		{
			name: "closed recovery",
			prepare: func(_ context.CancelFunc, sub *Subscriber) {
				queueRecovery(t, sub)
				sub.Close()
			},
			wantKind:    SubscriptionExitSubscriberClosed,
			wantQueued:  1,
			wantPending: true,
		},
		{
			name:           "caller cancellation precedes subscriber closure",
			initialCatchUp: true,
			prepare: func(cancel context.CancelFunc, sub *Subscriber) {
				sub.Close()
				cancel()
			},
			wantKind: SubscriptionExitContextCanceled,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sub := testSubscriber(t, "runner", nil, false, 1)
			tc.prepare(cancel, sub)
			openCalls := 0
			exit := RunSubscription(ctx, sub, tc.initialCatchUp, SubscriptionDriver{
				OpenReplay: func(context.Context, *cursor.ProgressMarker) (events.Iterator, error) {
					openCalls++
					return newSubscriptionIterator(), nil
				},
			})
			require.Equal(t, tc.wantKind, exit.Kind)
			require.Equal(t, SubscriptionPhaseReplay, exit.Phase)
			require.Zero(t, openCalls)
			require.Equal(t, tc.wantQueued, len(sub.Events()))
			require.Equal(t, tc.wantPending, sub.RecoveryPending())
		})
	}
}

func TestRunSubscriptionStopsBetweenReplayEvents(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stop     func(context.CancelFunc, *Subscriber)
		wantKind SubscriptionExitKind
	}{
		{
			name:     "context cancellation",
			stop:     func(cancel context.CancelFunc, _ *Subscriber) { cancel() },
			wantKind: SubscriptionExitContextCanceled,
		},
		{
			name:     "subscriber closure",
			stop:     func(_ context.CancelFunc, sub *Subscriber) { sub.Close() },
			wantKind: SubscriptionExitSubscriberClosed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sub := testSubscriber(t, "runner", nil, false, 1)
			first := subscriptionEvent(1)
			second := subscriptionEvent(2)
			iter := newSubscriptionIterator(first, second)
			var delivered []string

			exit := RunSubscription(ctx, sub, true, SubscriptionDriver{
				OpenReplay: func(context.Context, *cursor.ProgressMarker) (events.Iterator, error) {
					return iter, nil
				},
				Deliver: func(_ context.Context, evt *events.StoreChangeEvent, _ string) error {
					delivered = append(delivered, evt.EventID)
					tc.stop(cancel, sub)
					return nil
				},
			})

			require.Equal(t, tc.wantKind, exit.Kind)
			require.Equal(t, SubscriptionPhaseReplay, exit.Phase)
			require.Equal(t, []string{first.EventID}, delivered)
			require.Equal(t, first.EventID, sub.CurrentProgress().GetPosition("source"))
			require.Equal(t, 1, iter.closed)
		})
	}
}

func TestRunSubscriptionRechecksTerminationBeforeReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := testSubscriber(t, "runner", nil, false, 1)
	readyCalls := 0
	exit := RunSubscription(ctx, sub, false, SubscriptionDriver{
		Ready: func(context.Context, string) error {
			readyCalls++
			return nil
		},
		EnterLive: func() {
			sub.Close()
			cancel()
		},
	})

	require.Equal(t, SubscriptionExitContextCanceled, exit.Kind)
	require.Equal(t, SubscriptionPhaseReady, exit.Phase)
	require.Zero(t, readyCalls)
}

func queueRecovery(t *testing.T, sub *Subscriber) {
	t.Helper()
	admitted, recoveryStarted := sub.enqueue(subscriptionEvent(1))
	require.True(t, admitted)
	require.False(t, recoveryStarted)
	admitted, recoveryStarted = sub.enqueue(subscriptionEvent(2))
	require.False(t, admitted)
	require.True(t, recoveryStarted)
}

func TestRunSubscriptionRejectsNilEvents(t *testing.T) {
	t.Run("replay", func(t *testing.T) {
		iter := newSubscriptionIterator(nil)
		exit := RunSubscription(context.Background(), testSubscriber(t, "runner", nil, false, 1), true, SubscriptionDriver{
			OpenReplay: func(context.Context, *cursor.ProgressMarker) (events.Iterator, error) {
				return iter, nil
			},
		})
		require.Equal(t, SubscriptionExitReplayFailed, exit.Kind)
		require.Equal(t, SubscriptionPhaseReplay, exit.Phase)
		require.ErrorIs(t, exit.Err, ErrNilSubscriptionEvent)
		require.Equal(t, 1, iter.closed)
	})

	t.Run("live", func(t *testing.T) {
		sub := testSubscriber(t, "runner", nil, false, 1)
		admitted, _ := sub.enqueue(nil)
		require.True(t, admitted)
		exit := RunSubscription(context.Background(), sub, false, SubscriptionDriver{})
		require.Equal(t, SubscriptionExitDeliveryFailed, exit.Kind)
		require.Equal(t, SubscriptionPhaseLive, exit.Phase)
		require.ErrorIs(t, exit.Err, ErrNilSubscriptionEvent)
	})
}

func TestRunSubscriptionRunsMaintenanceOnlyAfterEnteringLive(t *testing.T) {
	maintenanceErr := errors.New("maintenance failed")
	ticks := make(chan time.Time, 1)
	order := make([]string, 0, 3)
	sub := testSubscriber(t, "runner", nil, false, 1)
	ticks <- time.Now()

	exit := RunSubscription(context.Background(), sub, false, SubscriptionDriver{
		Deliver: func(context.Context, *events.StoreChangeEvent, string) error {
			return nil
		},
		Ready: func(context.Context, string) error {
			order = append(order, "ready")
			return nil
		},
		Maintenance: ticks,
		Maintain: func(context.Context, string) error {
			order = append(order, "maintenance")
			return maintenanceErr
		},
		EnterLive: func() {
			order = append(order, "live")
		},
	})

	require.Equal(t, SubscriptionExitMaintenanceFailed, exit.Kind)
	require.Equal(t, SubscriptionPhaseMaintenance, exit.Phase)
	require.ErrorIs(t, exit.Err, maintenanceErr)
	require.Equal(t, []string{"live", "ready", "maintenance"}, order)
}

func TestSubscribeReadyReportsMalformedProgress(t *testing.T) {
	p := New(config.DefaultConfig(), nil)
	stream := p.SubscribeReady(context.Background(), "verified", "!invalid", nil)

	evt, open := <-stream
	require.True(t, open)
	require.Error(t, evt.Error)
	require.False(t, evt.Retryable)
	_, open = <-stream
	require.False(t, open)
	require.Zero(t, p.subs.Count())
}
