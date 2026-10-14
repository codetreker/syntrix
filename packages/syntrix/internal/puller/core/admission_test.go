package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/puller/buffer"
	"github.com/codetreker/syntrix/internal/puller/cursor"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

func TestOrdinaryAdmissionIdleSourceAndRecoveryAreProgressNeutral(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	p, b, _ := nativeBoundaryFixture(t)
	ch, state := p.SubscribeStatus(ctx, "idle", "")
	await := func(target string, generation uint64) *events.PullerEvent {
		for {
			select {
			case frame := <-ch:
				require.NotNil(t, frame)
				require.Nil(t, frame.Error)
				state.Observe(frame)
				if frame.AdmissionState == target && frame.AdmissionGeneration >= generation {
					return frame
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
				return nil
			}
		}
	}
	ready := await(events.AdmissionAdmitted, 1)
	require.Empty(t, ready.Progress)
	require.NoError(t, state.Err(ctx))
	b.setCaptureState(false, ErrCaptureUnavailable)
	recovery := await(events.AdmissionRecovering, 2)
	require.Empty(t, recovery.Progress)
	require.Error(t, state.Err(ctx))
	b.setCaptureState(true, nil)
	ready = await(events.AdmissionAdmitted, recovery.AdmissionGeneration)
	require.Empty(t, ready.Progress)
	require.NoError(t, state.Err(ctx))
	fatal := errors.New("failed checkpoint")
	b.setCaptureState(false, fatal)
	select {
	case frame := <-ch:
		require.ErrorIs(t, frame.Error, fatal)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Error(t, state.Err(ctx))
	require.ErrorIs(t, p.TerminalError(), fatal)
}

func TestPullerStopDrainsActualBoundedReplayBeforeBufferClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, b, _ := nativeBoundaryFixture(t)
	for n := 1; n <= 1002; n++ {
		event := nativeBoundaryEvent(n)
		require.NoError(t, b.buffer.Write(ctx, event, bson.Raw{5, 0, 0, 0, 0}))
	}
	require.NoError(t, b.buffer.Flush(ctx))
	marker := (&cursor.ProgressMarker{Positions: map[string]string{"source": ""}}).Encode()
	out := p.Subscribe(ctx, "backpressure", marker)
	require.Eventually(t, func() bool { return len(out) == 1000 }, time.Second, time.Millisecond)
	require.NoError(t, p.Stop(ctx), "replay iterator must close before Pebble")
	require.NoError(t, ctx.Err(), "Puller shutdown must not cancel caller context")
	for range out {
	}
	require.Zero(t, p.subs.Count())
	require.ErrorContains(t, p.Start(ctx), "stopped")
	stopped, status := p.SubscribeStatus(ctx, "stopped", "")
	frame := <-stopped
	require.ErrorIs(t, frame.Error, context.Canceled)
	require.True(t, status.Snapshot().Terminal)
}

func TestPullerAdmissionAndStopSerializeProducerOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, _, _ := nativeBoundaryFixture(t)
	start := make(chan struct{})
	var producers sync.WaitGroup
	for n := range 32 {
		producers.Add(1)
		go func() {
			defer producers.Done()
			<-start
			for range p.Subscribe(ctx, fmt.Sprint(n), "") {
			}
		}()
	}
	close(start)
	require.NoError(t, p.Stop(ctx))
	producers.Wait()
	require.NoError(t, p.Stop(ctx))
	require.NoError(t, ctx.Err())
	require.Zero(t, p.subs.Count())
}

func TestPullerStopDeadlineLeavesUnsettledProducerBuffersOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, b, _ := nativeBoundaryFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	p.watchFunc = func(ctx context.Context, _ *Backend, _ *slog.Logger) error {
		close(entered)
		<-ctx.Done()
		<-release
		return ctx.Err()
	}
	b.cleaner = buffer.NewCleaner(buffer.CleanerOptions{Buffer: b.buffer, Interval: time.Hour})
	require.NoError(t, p.Start(ctx))
	require.ErrorContains(t, p.Start(ctx), "already started")
	<-entered
	deadline, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	require.ErrorIs(t, p.Stop(deadline), context.DeadlineExceeded)
	require.ErrorIs(t, p.Readiness(ctx), context.Canceled, "stopping cannot remain serving while buffers are unsettled")
	_, err := b.buffer.Head()
	require.NoError(t, err, "unsettled native owner retains buffer")
	close(release)
	require.NoError(t, p.Stop(ctx))
	_, err = b.buffer.Head()
	require.Error(t, err)
}

func TestNativeCaptureFailureDoesNotCloseSubscriptionOwnedBuffer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, b, _ := nativeBoundaryFixture(t)
	b.cleaner = buffer.NewCleaner(buffer.CleanerOptions{Buffer: b.buffer, Interval: time.Hour})
	p.watchFunc = func(context.Context, *Backend, *slog.Logger) error {
		return fmt.Errorf("%w: native failure", errCaptureFailed)
	}
	require.NoError(t, p.Start(ctx))
	require.Eventually(t, func() bool { return p.TerminalError() != nil }, time.Second, time.Millisecond)
	_, err := b.buffer.Head()
	require.NoError(t, err, "native exit must not bypass producer settlement")
	require.NoError(t, p.Stop(ctx))
}

func TestAdmissionDrainsPrefixAndRearmsOverflowWithoutReplacingSubscriber(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sub, err := NewSubscriber("overflow", cursor.NewProgressMarker(), true, false, 1)
	require.NoError(t, err)
	initial, second, third := subscriptionEvent(1), subscriptionEvent(2), subscriptionEvent(3)
	sub.enqueue(initial)
	var delivered []string
	var controls []string
	generation := uint64(0)
	driver := SubscriptionDriver{
		CheckAdmission: func(context.Context) error { return nil },
		OpenAdmissionReplay: func(_ context.Context, _ *cursor.ProgressMarker, floors map[string]events.ClusterTime) (events.Iterator, error) {
			require.Equal(t, initial.ClusterTime, floors["source"])
			return newSubscriptionIterator(initial, second, third), nil
		},
		Deliver: func(_ context.Context, e *events.StoreChangeEvent, _ string) error {
			delivered = append(delivered, e.EventID)
			if e == initial {
				sub.enqueue(second)
				sub.enqueue(third)
			}
			return nil
		},
		Admission: func(_ context.Context, state string, g uint64, _ string) error {
			controls = append(controls, state)
			generation = g
			if state == events.AdmissionAdmitted {
				require.Equal(t, []string{initial.EventID, second.EventID, third.EventID}, delivered)
				cancel()
			}
			return nil
		},
	}
	exit := RunSubscription(ctx, sub, false, driver)
	require.Equal(t, SubscriptionExitContextCanceled, exit.Kind)
	require.GreaterOrEqual(t, generation, uint64(2))
	require.Contains(t, controls, events.AdmissionRecovering)
	require.Equal(t, initial.ClusterTime, sub.AdmissionFloors()["source"])
}

func TestPullerReadinessRequiresCaptureAndUsableDurableCheckpoint(t *testing.T) {
	for _, phase := range []string{"healthy", "inactive", "capture-failed", "missing-checkpoint", "closed-buffer", "canceled", "stopped", "unconfigured"} {
		t.Run(phase, func(t *testing.T) {
			p, b, _ := nativeBoundaryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var expected error
			switch phase {
			case "inactive":
				b.setCaptureState(false, nil)
				expected = ErrCaptureUnavailable
			case "capture-failed":
				expected = errors.New("checkpoint persistence failed")
				b.setCaptureState(false, expected)
			case "missing-checkpoint":
				require.NoError(t, b.buffer.DeleteCheckpoint())
				expected = ErrCaptureUnavailable
			case "closed-buffer":
				require.NoError(t, b.buffer.Close())
			case "canceled":
				cancel()
				expected = context.Canceled
			case "stopped":
				p.stopLifetime()
				expected = context.Canceled
			case "unconfigured":
				delete(p.backends, b.name)
				expected = ErrCaptureUnavailable
			}
			err := p.Readiness(ctx)
			if phase == "closed-buffer" {
				require.ErrorContains(t, err, "buffer is closed")
			} else {
				require.ErrorIs(t, err, expected)
			}
		})
	}
}

func TestOrdinaryAdmissionSurvivesRetentionAndBufferReopen(t *testing.T) {
	for _, mode := range []string{"age", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			p, backend, origin := nativeBoundaryFixture(t)
			original := backend.buffer
			token := bson.Raw{5, 0, 0, 0, 0}
			oldTime := uint32(1)
			cleanerOptions := buffer.CleanerOptions{Buffer: original, Retention: time.Second}
			if mode == "capacity" {
				oldTime = uint32(time.Now().Unix()) - 2
				cleanerOptions.Retention, cleanerOptions.MaxSize = time.Hour, 1
			}
			old := &events.StoreChangeEvent{Backend: backend.name, EventID: fmt.Sprintf("%d-1-old", oldTime), ClusterTime: events.ClusterTime{T: oldTime, I: 1}}
			tail := &events.StoreChangeEvent{Backend: backend.name, EventID: fmt.Sprintf("%d-1-tail", oldTime+1), ClusterTime: events.ClusterTime{T: oldTime + 1, I: 1}}
			for _, event := range []*events.StoreChangeEvent{old, tail} {
				require.NoError(t, original.Write(ctx, event, token))
			}
			require.NoError(t, original.Flush(ctx))
			lineage := original.Lineage()
			retained := (&cursor.ProgressMarker{Positions: map[string]string{backend.name: tail.EventID}, Lineages: map[string]string{backend.name: lineage}}).Encode()
			removed := (&cursor.ProgressMarker{Positions: map[string]string{backend.name: old.EventID}, Lineages: map[string]string{backend.name: lineage}}).Encode()
			awaitAdmitted := func(ch <-chan *events.PullerEvent, state *events.SubscriptionStatus) {
				for {
					select {
					case frame, open := <-ch:
						require.True(t, open, "retention terminated ordinary ingestion")
						require.NotNil(t, frame)
						require.NoError(t, frame.Error)
						state.Observe(frame)
						if frame.Ready {
							require.NoError(t, state.Err(ctx))
							require.False(t, state.Snapshot().Terminal)
							return
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			}
			stopSubscription := func(cancelSubscription context.CancelFunc, ch <-chan *events.PullerEvent) {
				cancelSubscription()
				for {
					select {
					case _, open := <-ch:
						if !open {
							return
						}
					case <-ctx.Done():
						t.Fatal("subscription did not stop")
					}
				}
			}
			subscriptionContext, cancelSubscription := context.WithCancel(ctx)
			ch, state := p.SubscribeStatus(subscriptionContext, "retention", "")
			defer stopSubscription(cancelSubscription, ch)
			awaitAdmitted(ch, state)
			require.NoError(t, buffer.NewCleaner(cleanerOptions).CleanupNow(ctx))
			count, err := original.Count()
			require.NoError(t, err)
			require.Equal(t, 1, count, "cleanup must actually remove an older group")
			require.ErrorContains(t, original.ValidatePosition("", lineage), "expired")
			require.NoError(t, p.Readiness(ctx))
			require.ErrorContains(t, p.ValidateBoundary(ctx, origin), "expired")
			require.ErrorContains(t, p.ValidateBoundary(ctx, removed), "expired")
			require.NoError(t, p.ValidateBoundary(ctx, retained))
			awaitAdmitted(ch, state)
			stopSubscription(cancelSubscription, ch)
			backend.setCaptureState(false, ErrCaptureUnavailable)
			require.NoError(t, original.Close())
			reopened, err := buffer.New(buffer.Options{Path: original.Path()})
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.Close()) }()
			backend.buffer = reopened
			backend.setCaptureState(true, nil)
			require.Equal(t, lineage, reopened.Lineage())
			restored, err := reopened.LoadCheckpoint()
			require.NoError(t, err)
			require.Equal(t, token, restored)
			require.ErrorContains(t, reopened.ValidatePosition("", lineage), "expired")
			require.NoError(t, p.Readiness(ctx))
			require.ErrorContains(t, p.ValidateBoundary(ctx, origin), "expired")
			require.ErrorContains(t, p.ValidateBoundary(ctx, removed), "expired")
			iter, err := p.ReplayBoundary(ctx, retained, false)
			require.NoError(t, err)
			require.True(t, iter.Next())
			require.Equal(t, tail.EventID, iter.Event().EventID)
			require.False(t, iter.Next())
			require.NoError(t, iter.Err())
			require.NoError(t, iter.Close())
			subscriptionContext, cancelSubscription = context.WithCancel(ctx)
			ch, state = p.SubscribeStatus(subscriptionContext, "reopened", "")
			defer stopSubscription(cancelSubscription, ch)
			awaitAdmitted(ch, state)
			awaitAdmitted(ch, state)
			stopSubscription(cancelSubscription, ch)
		})
	}
}

func TestAdmissionFenceFailureNeverPublishesReady(t *testing.T) {
	for _, cancelFence := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "canceled"}[cancelFence], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			sub := testSubscriber(t, "fence", cursor.NewProgressMarker(), false, 1)
			cause := errors.New("unsettled ingress")
			var controls []string
			driver := SubscriptionDriver{
				Admission: func(_ context.Context, state string, _ uint64, _ string) error {
					controls = append(controls, state)
					return nil
				},
				FenceAdmission: func(context.Context) error {
					if cancelFence {
						cancel()
						return context.Canceled
					}
					return cause
				},
			}
			exit := RunSubscription(ctx, sub, false, driver)
			if cancelFence {
				require.Equal(t, SubscriptionExitContextCanceled, exit.Kind)
				require.ErrorIs(t, exit.Err, context.Canceled)
			} else {
				require.Equal(t, SubscriptionExitReadyFailed, exit.Kind)
				require.ErrorIs(t, exit.Err, cause)
			}
			require.Equal(t, []string{events.AdmissionStarting}, controls)
		})
	}
}
