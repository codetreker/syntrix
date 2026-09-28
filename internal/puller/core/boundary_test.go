package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/puller/buffer"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

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

func nativeBoundaryFixture(t *testing.T) (*Puller, *Backend, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	b, err := buffer.New(buffer.Options{Path: t.TempDir(), Logger: logger})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	p := New(config.DefaultConfig(), logger)
	backend := &Backend{name: "source", buffer: b}
	backend.setCaptureState(true, nil)
	p.backends[backend.name] = backend
	require.NoError(t, b.SaveCheckpoint(bson.Raw{5, 0, 0, 0, 0}))
	boundary, err := p.BootstrapBoundary(context.Background())
	require.NoError(t, err)
	return p, backend, boundary
}

func nativeBoundaryEvent(index int) *events.StoreChangeEvent {
	return &events.StoreChangeEvent{Backend: "source", EventID: fmt.Sprintf("1-%d-event", index), ClusterTime: events.ClusterTime{T: 1, I: uint32(index)}}
}

func TestNativeBoundaryRejectsMalformedAndForeignBackendInventories(t *testing.T) {
	t.Parallel()
	p, _, boundary := nativeBoundaryFixture(t)
	valid, err := cursor.DecodeProgressMarker(boundary)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		marker  string
		message string
	}{
		{"encoding", "!invalid-base64", "base64"},
		{"missing-inventory", "", "backend set"},
		{"foreign-backend", (&cursor.ProgressMarker{Positions: map[string]string{"foreign": ""}, Lineages: map[string]string{"foreign": "lineage"}}).Encode(), "missing backend"},
		{"missing-lineage", (&cursor.ProgressMarker{Positions: valid.Positions, Lineages: map[string]string{"source": ""}}).Encode(), "missing lineage"},
		{"invalid-position", (&cursor.ProgressMarker{Positions: map[string]string{"source": "bad-event-id"}, Lineages: valid.Lineages}).Encode(), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := p.ValidateBoundary(context.Background(), tc.marker)
			require.Error(t, err)
			if tc.message != "" {
				require.Contains(t, err.Error(), tc.message)
			}
			iter, err := p.ReplayBoundary(context.Background(), tc.marker, false)
			require.Error(t, err)
			require.Nil(t, iter)
		})
	}
}

func TestBootstrapRejectsUnavailableOrNonemptyCapture(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"unconfigured", "capture-error", "capture-canceled", "closed-buffer", "missing-checkpoint", "nonempty"} {
		t.Run(phase, func(t *testing.T) {
			p, backend, boundary := nativeBoundaryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			switch phase {
			case "unconfigured":
				delete(p.backends, backend.name)
			case "capture-error":
				backend.setCaptureState(false, ErrCaptureUnavailable)
				require.ErrorIs(t, p.ValidateBoundary(ctx, boundary), ErrCaptureUnavailable)
			case "capture-canceled":
				backend.setCaptureState(false, nil)
				cancel()
				require.ErrorIs(t, p.ValidateBoundary(ctx, boundary), context.Canceled)
			case "closed-buffer":
				require.NoError(t, backend.buffer.Close())
			case "missing-checkpoint":
				require.NoError(t, backend.buffer.DeleteCheckpoint())
			case "nonempty":
				require.NoError(t, backend.buffer.Write(ctx, nativeBoundaryEvent(1), bson.Raw{5, 0, 0, 0, 0}))
			}
			got, err := p.BootstrapBoundary(ctx)
			require.Error(t, err)
			require.Empty(t, got)
		})
	}
}

func TestVerifiedNativeSubscriptionReportsPermanentAndTemporaryFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"admission", "health"} {
		for _, temporary := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/temporary=%t", phase, temporary), func(t *testing.T) {
				t.Parallel()
				p, backend, boundary := nativeBoundaryFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				failure := errors.New("capture permanently failed")
				if temporary {
					failure = fmt.Errorf("source offline: %w", ErrCaptureUnavailable)
				}
				if phase == "admission" {
					backend.setCaptureState(false, failure)
				}
				stream := p.SubscribeReady(ctx, "indexer", boundary, nil)
				if phase == "health" {
					select {
					case evt := <-stream:
						require.True(t, evt.Ready)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					backend.setCaptureState(false, failure)
				}
				select {
				case evt := <-stream:
					require.NotNil(t, evt)
					require.ErrorIs(t, evt.Error, failure)
					require.Equal(t, temporary, evt.Retryable)
					require.False(t, evt.Ready)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case _, open := <-stream:
					require.False(t, open)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				require.Zero(t, p.subs.Count())
			})
		}
	}
}

func TestNativeReplayCorruptionNeverAnnouncesReady(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	b, err := buffer.New(buffer.Options{Path: path, Logger: logger})
	require.NoError(t, err)
	require.NoError(t, b.SaveCheckpoint(bson.Raw{5, 0, 0, 0, 0}))
	lineage := b.Lineage()
	require.NoError(t, b.Close())
	db, err := pebble.Open(path, &pebble.Options{})
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte(nativeBoundaryEvent(1).BufferKey()), []byte("corrupt event payload"), pebble.Sync))
	require.NoError(t, db.Close())
	b, err = buffer.New(buffer.Options{Path: path, Logger: logger})
	require.NoError(t, err)
	defer b.Close()
	p := New(config.DefaultConfig(), logger)
	backend := &Backend{name: "source", buffer: b}
	backend.setCaptureState(true, nil)
	p.backends[backend.name] = backend
	marker := (&cursor.ProgressMarker{Positions: map[string]string{"source": ""}, Lineages: map[string]string{"source": lineage}}).Encode()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := p.SubscribeReady(ctx, "indexer", marker, nil)
	select {
	case failure := <-stream:
		require.NotNil(t, failure)
		require.ErrorContains(t, failure.Error, "unmarshal event")
		require.False(t, failure.Retryable)
		require.False(t, failure.Ready)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case _, open := <-stream:
		require.False(t, open)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestUnverifiedNativeSubscriptionRejectsInvalidMarkerAndClosedReplay(t *testing.T) {
	t.Parallel()
	p, backend, marker := nativeBoundaryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, after := range []string{"!invalid", marker} {
		if after == marker {
			require.NoError(t, backend.buffer.Close())
		}
		select {
		case _, open := <-p.Subscribe(ctx, "consumer", after):
			require.False(t, open)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestVerifiedNativeSubscriptionRecoversOverflowWithoutLosingHistory(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"replay", "live"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			p, backend, boundary := nativeBoundaryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			const count = 1001
			for i := 1; i <= count; i++ {
				require.NoError(t, backend.buffer.Write(ctx, nativeBoundaryEvent(i), bson.Raw{5, 0, 0, 0, 0}))
			}
			require.NoError(t, backend.buffer.Flush(ctx))
			after := boundary
			entered, release := make(chan struct{}), make(chan struct{})
			onReady := func(string) {}
			if phase == "live" {
				marker, err := cursor.DecodeProgressMarker(boundary)
				require.NoError(t, err)
				marker.Positions["source"] = nativeBoundaryEvent(count).EventID
				after = marker.Encode()
				onReady = func(string) {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
			}
			stream := p.SubscribeReady(ctx, "indexer", after, onReady)
			if phase == "replay" {
				require.Eventually(t, func() bool { return len(stream) == cap(stream) }, time.Second, time.Millisecond)
				for i := 1; i <= count; i++ {
					p.subs.Broadcast(nativeBoundaryEvent(i))
				}
			} else {
				select {
				case replayed := <-stream:
					require.NotNil(t, replayed.Change)
					require.Equal(t, nativeBoundaryEvent(count).EventID, replayed.Change.EventID)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case ready := <-stream:
					require.True(t, ready.Ready)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				for i := count + 1; i <= 2*count; i++ {
					evt := nativeBoundaryEvent(i)
					require.NoError(t, backend.buffer.Write(ctx, evt, bson.Raw{5, 0, 0, 0, 0}))
					p.subs.Broadcast(evt)
				}
				require.NoError(t, backend.buffer.Flush(ctx))
				close(release)
			}
			for i := 1; i <= count; i++ {
				want := i
				if phase == "live" {
					want += count
				}
				select {
				case evt := <-stream:
					require.NotNil(t, evt)
					require.NoError(t, evt.Error)
					require.NotNil(t, evt.Change)
					require.Equal(t, nativeBoundaryEvent(want).EventID, evt.Change.EventID)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if phase == "replay" {
				select {
				case ready := <-stream:
					require.True(t, ready.Ready)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			cancel()
			for evt := range stream {
				t.Errorf("unexpected duplicate after completed recovery: %+v", evt)
			}
			require.Zero(t, p.subs.Count())
		})
	}
}

func TestNativeSubscriptionWakesForOverflowBeforeBroadcastReturns(t *testing.T) {
	p, backend, _ := nativeBoundaryFixture(t)
	logger, blockedWarn := newBlockingWarnLogger()
	t.Cleanup(blockedWarn.unblock)
	p.subs = NewSubscriberManager(logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const last = 2002
	for i := 1; i <= last; i++ {
		require.NoError(t, backend.buffer.Write(ctx, nativeBoundaryEvent(i), bson.Raw{5, 0, 0, 0, 0}))
	}
	require.NoError(t, backend.buffer.Flush(ctx))

	stream := p.Subscribe(ctx, "late-overflow", "")
	require.Eventually(t, func() bool { return p.subs.Count() == 1 }, time.Second, time.Millisecond)
	for i := 1; i <= 1000; i++ {
		p.subs.Broadcast(nativeBoundaryEvent(i))
	}
	require.Eventually(t, func() bool { return len(stream) == cap(stream) }, time.Second, time.Millisecond)

	sub := p.subs.All()[0]
	p.subs.Broadcast(nativeBoundaryEvent(1001))
	require.Eventually(t, func() bool { return len(sub.Events()) == 0 }, time.Second, time.Millisecond)
	for i := 1002; i <= 2001; i++ {
		p.subs.Broadcast(nativeBoundaryEvent(i))
	}
	require.Equal(t, cap(sub.Events()), len(sub.Events()))

	overflowDone := make(chan struct{})
	go func() {
		defer close(overflowDone)
		p.subs.Broadcast(nativeBoundaryEvent(last))
	}()
	select {
	case <-blockedWarn.entered:
	case <-ctx.Done():
		t.Fatal("overflow logging did not block")
	}

	for i := 1; i <= last; i++ {
		select {
		case evt := <-stream:
			require.NotNil(t, evt.Change)
			require.Equal(t, nativeBoundaryEvent(i).EventID, evt.Change.EventID)
		case <-ctx.Done():
			t.Fatalf("subscription stopped before event %d: %v", i, ctx.Err())
		}
	}

	blockedWarn.unblock()
	select {
	case <-overflowDone:
	case <-ctx.Done():
		t.Fatal("overflow broadcast did not finish")
	}
}

func TestNativeSubscriptionStartFromNowOverflowBeforeFirstDelivery(t *testing.T) {
	p, backend, _ := nativeBoundaryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, backend.buffer.Write(ctx, nativeBoundaryEvent(1), bson.Raw{5, 0, 0, 0, 0}))
	require.NoError(t, backend.buffer.Flush(ctx))

	enteredLive := make(chan struct{})
	release := make(chan struct{})
	stream := p.subscribe(ctx, "current-head-overflow", "", func(string) {
		close(enteredLive)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}, false)
	select {
	case <-enteredLive:
	case <-ctx.Done():
		t.Fatal("subscription did not enter live mode")
	}

	const last = 1002
	for i := 2; i <= last; i++ {
		evt := nativeBoundaryEvent(i)
		require.NoError(t, backend.buffer.Write(ctx, evt, bson.Raw{5, 0, 0, 0, 0}))
		p.subs.Broadcast(evt)
	}
	require.NoError(t, backend.buffer.Flush(ctx))
	sub := p.subs.All()[0]
	require.True(t, sub.RecoveryPending())
	require.Empty(t, sub.CurrentProgress().Positions)
	sub.drainEvents()
	close(release)

	for i := 2; i <= last; i++ {
		select {
		case evt := <-stream:
			require.NotNil(t, evt)
			require.NotNil(t, evt.Change)
			require.Equal(t, nativeBoundaryEvent(i).EventID, evt.Change.EventID)
		case <-ctx.Done():
			t.Fatalf("subscription stopped before event %d: %v", i, ctx.Err())
		}
	}
}

func TestReplayFromAdmissionSeeksPastOldBackends(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	corruptHistory := func(name string, old *events.StoreChangeEvent) *Backend {
		t.Helper()
		path := t.TempDir()
		b, err := buffer.New(buffer.Options{Path: path, Logger: logger})
		require.NoError(t, err)
		require.NoError(t, b.Write(ctx, old, bson.Raw{5, 0, 0, 0, 0}))
		require.NoError(t, b.Flush(ctx))
		require.NoError(t, b.Close())
		db, err := pebble.Open(path, &pebble.Options{})
		require.NoError(t, err)
		require.NoError(t, db.Set([]byte(old.BufferKey()), []byte("corrupt old event"), pebble.Sync))
		require.NoError(t, db.Close())
		b, err = buffer.New(buffer.Options{Path: path, Logger: logger})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, b.Close()) })
		return &Backend{name: name, buffer: b}
	}

	p := New(config.DefaultConfig(), logger)
	oldActive := &events.StoreChangeEvent{Backend: "active", EventID: "1-1-old", ClusterTime: events.ClusterTime{T: 1, I: 1}}
	oldQuiet := &events.StoreChangeEvent{Backend: "quiet", EventID: "1-1-old", ClusterTime: events.ClusterTime{T: 1, I: 1}}
	p.backends["active"] = corruptHistory("active", oldActive)
	p.backends["quiet"] = corruptHistory("quiet", oldQuiet)
	newEvent := &events.StoreChangeEvent{Backend: "active", EventID: "2-1-new", ClusterTime: events.ClusterTime{T: 2, I: 1}}
	require.NoError(t, p.backends["active"].buffer.Write(ctx, newEvent, bson.Raw{5, 0, 0, 0, 0}))
	require.NoError(t, p.backends["active"].buffer.Flush(ctx))

	iter, err := p.ReplayFromAdmission(ctx, nil, map[string]events.ClusterTime{"active": newEvent.ClusterTime})
	require.NoError(t, err)
	require.True(t, iter.Next())
	require.Equal(t, newEvent.EventID, iter.Event().EventID)
	require.False(t, iter.Next())
	require.NoError(t, iter.Err())
	require.NoError(t, iter.Close())
	later := &events.StoreChangeEvent{Backend: "active", EventID: "3-1-later", ClusterTime: events.ClusterTime{T: 3, I: 1}}
	require.NoError(t, p.backends["active"].buffer.Write(ctx, later, bson.Raw{5, 0, 0, 0, 0}))
	require.NoError(t, p.backends["active"].buffer.Flush(ctx))
	iter, err = p.ReplayFromAdmission(ctx, map[string]string{"active": later.EventID}, map[string]events.ClusterTime{"active": newEvent.ClusterTime})
	require.NoError(t, err)
	require.True(t, iter.Next())
	require.Equal(t, later.EventID, iter.Event().EventID)
	require.False(t, iter.Next())
	require.NoError(t, iter.Err())
	require.NoError(t, iter.Close())

	iter, err = p.ReplayFromAdmission(ctx, nil, map[string]events.ClusterTime{})
	require.NoError(t, err)
	require.False(t, iter.Next())
	require.NoError(t, iter.Close())
	_, err = p.ReplayFromAdmission(ctx, nil, nil)
	require.ErrorContains(t, err, "requires backend floors")
}

func TestNativeSubscriptionCancellationWithRecoveryPending(t *testing.T) {
	logger, blockedWarn := newBlockingWarnLogger()
	t.Cleanup(blockedWarn.unblock)
	p := New(config.DefaultConfig(), logger)
	ctx, cancel := context.WithCancel(context.Background())
	stream := p.Subscribe(ctx, "cancel-overflow", "")
	require.Eventually(t, func() bool { return p.subs.Count() == 1 }, time.Second, time.Millisecond)

	for i := 1; i <= 1000; i++ {
		p.subs.Broadcast(nativeBoundaryEvent(i))
	}
	require.Eventually(t, func() bool { return len(stream) == cap(stream) }, time.Second, time.Millisecond)
	sub := p.subs.All()[0]
	p.subs.Broadcast(nativeBoundaryEvent(1001))
	require.Eventually(t, func() bool { return len(sub.Events()) == 0 }, time.Second, time.Millisecond)
	for i := 1002; i <= 2001; i++ {
		p.subs.Broadcast(nativeBoundaryEvent(i))
	}

	overflowDone := make(chan struct{})
	go func() {
		defer close(overflowDone)
		p.subs.Broadcast(nativeBoundaryEvent(2002))
	}()
	select {
	case <-blockedWarn.entered:
	case <-time.After(time.Second):
		t.Fatal("overflow logging did not block")
	}
	require.True(t, sub.RecoveryPending())
	cancel()
	blockedWarn.unblock()
	select {
	case <-overflowDone:
	case <-time.After(time.Second):
		t.Fatal("overflow broadcast did not finish")
	}

	deadline := time.After(time.Second)
	for {
		select {
		case _, open := <-stream:
			if !open {
				require.Zero(t, p.subs.Count())
				return
			}
		case <-deadline:
			t.Fatal("subscription did not terminate with recovery pending")
		}
	}
}

func TestNativeSlowConsumerCancellationReleasesSubscription(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"replay", "ready", "live"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			p, backend, boundary := nativeBoundaryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			count := 1001
			if phase == "ready" {
				count = 1000
			}
			if phase != "live" {
				for i := 1; i <= count; i++ {
					require.NoError(t, backend.buffer.Write(ctx, nativeBoundaryEvent(i), bson.Raw{5, 0, 0, 0, 0}))
				}
				require.NoError(t, backend.buffer.Flush(ctx))
			}
			stream := p.SubscribeReady(ctx, "slow-consumer", boundary, nil)
			if phase == "live" {
				select {
				case ready := <-stream:
					require.True(t, ready.Ready)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				for i := 1; i < count; i++ {
					evt := nativeBoundaryEvent(i)
					require.NoError(t, backend.buffer.Write(ctx, evt, bson.Raw{5, 0, 0, 0, 0}))
					p.subs.Broadcast(evt)
				}
			}
			require.Eventually(t, func() bool { return len(stream) == cap(stream) }, time.Second, time.Millisecond)
			if phase == "live" {
				evt := nativeBoundaryEvent(count)
				require.NoError(t, backend.buffer.Write(ctx, evt, bson.Raw{5, 0, 0, 0, 0}))
				p.subs.Broadcast(evt)
				var blocked *Subscriber
				require.Eventually(t, func() bool {
					subs := p.subs.All()
					if len(subs) != 1 {
						return false
					}
					blocked = subs[0]
					return len(blocked.Events()) == 0 && blocked.CurrentProgress().Positions["source"] == nativeBoundaryEvent(count-1).EventID
				}, time.Second, time.Millisecond)
				cancel()
				require.Eventually(t, func() bool { return p.subs.Count() == 0 }, time.Second, time.Millisecond)
				require.Equal(t, nativeBoundaryEvent(count-1).EventID, blocked.CurrentProgress().Positions["source"])
			}
			cancel()
			require.Eventually(t, func() bool { return p.subs.Count() == 0 }, time.Second, time.Millisecond)
			received := 0
			for evt := range stream {
				require.False(t, evt.Ready)
				require.NotNil(t, evt.Change)
				received++
			}
			require.Equal(t, cap(stream), received)
		})
	}
}

type idleBoundaryStream struct {
	token  bson.Raw
	ctx    context.Context
	polled bool
}

func (s *idleBoundaryStream) TryNext(ctx context.Context) bool {
	s.ctx = ctx
	if !s.polled {
		s.polled = true
		return false
	}
	<-ctx.Done()
	return false
}
func (s *idleBoundaryStream) ID() int64                   { return 1 }
func (s *idleBoundaryStream) ResumeToken() bson.Raw       { return s.token }
func (s *idleBoundaryStream) Decode(any) error            { return nil }
func (s *idleBoundaryStream) Err() error                  { return s.ctx.Err() }
func (s *idleBoundaryStream) Close(context.Context) error { return nil }

func TestNativeEmptyBoundarySurvivesRestartAndReplaysBeforeReady(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	b, err := buffer.New(buffer.Options{Path: dir, BatchInterval: time.Hour})
	require.NoError(t, err)
	p := New(config.DefaultConfig(), nil)
	backend := &Backend{name: "source", buffer: b}
	p.backends["source"] = backend
	token := bson.Raw{5, 0, 0, 0, 0}
	p.openStream = func(context.Context, *mongo.Database, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
		return &idleBoundaryStream{token: token}, nil
	}
	captureCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.watchChangeStream(captureCtx, backend, p.logger) }()
	boundary, err := p.BootstrapBoundary(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, boundary)
	pm, err := cursor.DecodeProgressMarker(boundary)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"source": ""}, pm.Positions)
	require.Equal(t, b.Lineage(), pm.Lineages["source"])
	stop()
	require.ErrorIs(t, <-done, context.Canceled)
	require.NoError(t, b.Close())
	b, err = buffer.New(buffer.Options{Path: dir})
	require.NoError(t, err)
	defer b.Close()
	restored, err := b.LoadCheckpoint()
	require.NoError(t, err)
	require.Equal(t, token, restored)
	backend.buffer = b
	backend.setCaptureState(true, nil)
	evt := &events.StoreChangeEvent{Backend: "source", EventID: "1-1-a", ClusterTime: events.ClusterTime{T: 1, I: 1}}
	require.NoError(t, b.Write(ctx, evt, token))
	require.NoError(t, b.Flush(ctx))
	ready := make(chan string, 1)
	subscription := p.SubscribeReady(ctx, "indexer", boundary, func(progress string) { ready <- progress })
	select {
	case received := <-subscription:
		require.Equal(t, evt.EventID, received.Change.EventID)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case progress := <-ready:
		require.NoError(t, p.ValidateBoundary(ctx, progress))
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, b.Delete(evt.BufferKey()))
	require.ErrorContains(t, p.ValidateBoundary(ctx, boundary), "expired")
	wrong := pm.Clone()
	wrong.Lineages["source"] = "changed"
	require.ErrorContains(t, p.ValidateBoundary(ctx, wrong.Encode()), "lineage")
	_, err = p.BootstrapBoundary(ctx)
	require.Error(t, err)
}

type gatedBoundaryStream struct {
	idleBoundaryStream
	entered chan struct{}
	release chan struct{}
}

func (s *gatedBoundaryStream) TryNext(ctx context.Context) bool {
	if !s.polled {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			s.ctx = ctx
			return false
		}
	}
	return s.idleBoundaryStream.TryNext(ctx)
}

func TestNativeBoundaryDoesNotTrustCachedResumeTokenBeforeLiveBatch(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, err := buffer.New(buffer.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer b.Close()
	p := New(config.DefaultConfig(), nil)
	backend := &Backend{name: "source", buffer: b}
	p.backends["source"] = backend
	stream := &gatedBoundaryStream{idleBoundaryStream: idleBoundaryStream{token: bson.Raw{5, 0, 0, 0, 0}}, entered: make(chan struct{}), release: make(chan struct{})}
	p.openStream = func(context.Context, *mongo.Database, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
		return stream, nil
	}
	done := make(chan error, 1)
	go func() { done <- p.watchChangeStream(ctx, backend, p.logger) }()
	select {
	case <-stream.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	token, err := b.LoadCheckpoint()
	require.NoError(t, err)
	require.Empty(t, token)
	backend.captureMu.Lock()
	active := backend.captureActive
	backend.captureMu.Unlock()
	require.False(t, active)
	close(stream.release)
	boundary, err := p.BootstrapBoundary(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, boundary)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}
