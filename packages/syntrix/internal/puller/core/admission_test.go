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
	p, b, _ := nativeBoundaryFixture(t)
	require.NoError(t, p.Readiness(context.Background()))
	b.setCaptureState(false, nil)
	require.ErrorIs(t, p.Readiness(context.Background()), ErrCaptureUnavailable)
	b.setCaptureState(true, nil)
	require.NoError(t, b.buffer.Close())
	require.ErrorContains(t, p.Readiness(context.Background()), "buffer")
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
