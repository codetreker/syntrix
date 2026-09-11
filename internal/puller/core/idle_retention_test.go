package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/puller/buffer"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"go.mongodb.org/mongo-driver/bson"
)

func idleRetentionEvent(timestamp uint32, suffix string) *events.StoreChangeEvent {
	return &events.StoreChangeEvent{Backend: "source", EventID: fmt.Sprintf("%d-1-%s", timestamp, suffix), ClusterTime: events.ClusterTime{T: timestamp, I: 1}}
}

func idleRetentionReceive(t *testing.T, ctx context.Context, stream <-chan *events.PullerEvent) *events.PullerEvent {
	t.Helper()
	select {
	case evt, open := <-stream:
		require.True(t, open, "verified subscription closed during idle retention")
		require.NotNil(t, evt)
		require.NoError(t, evt.Error)
		return evt
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return nil
	}
}

func TestIdleRetentionPreservesNativeReadyBoundaryAcrossCleanupAndRestart(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"age", "size"} {
		t.Run(policy, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			path := t.TempDir()
			b, err := buffer.New(buffer.Options{Path: path, Logger: logger})
			require.NoError(t, err)
			defer func() { require.NoError(t, b.Close()) }()
			p := New(config.DefaultConfig(), logger)
			backend := &Backend{name: "source", buffer: b}
			backend.setCaptureState(true, nil)
			p.backends[backend.name] = backend
			require.NoError(t, b.SaveCheckpoint(bson.Raw{5, 0, 0, 0, 0}))
			initial, err := p.BootstrapBoundary(ctx)
			require.NoError(t, err)
			lineage := b.Lineage()
			oldTime := uint32(time.Now().Add(-2 * time.Hour).Unix())
			latestTime := oldTime + 3600
			old := idleRetentionEvent(oldTime, "old")
			var lastToken bson.Raw
			write := func(evt *events.StoreChangeEvent) {
				token, err := bson.Marshal(bson.M{"_data": evt.EventID})
				require.NoError(t, err)
				lastToken = token
				require.NoError(t, b.Write(ctx, evt, token))
			}
			write(old)
			for _, suffix := range []string{"c", "a", "b"} {
				write(idleRetentionEvent(latestTime, suffix))
			}
			require.NoError(t, b.Flush(ctx))
			firstCtx, stopFirst := context.WithCancel(ctx)
			defer stopFirst()
			first := p.SubscribeReady(firstCtx, "indexer", initial, nil)
			for _, id := range []string{old.EventID, idleRetentionEvent(latestTime, "a").EventID, idleRetentionEvent(latestTime, "b").EventID, idleRetentionEvent(latestTime, "c").EventID} {
				evt := idleRetentionReceive(t, ctx, first)
				require.NotNil(t, evt.Change)
				require.Equal(t, id, evt.Change.EventID)
			}
			ready := idleRetentionReceive(t, ctx, first)
			require.True(t, ready.Ready)
			current := ready.Progress
			options := buffer.CleanerOptions{Buffer: b, Retention: time.Minute, Logger: logger}
			if policy == "size" {
				options.Retention = 24 * time.Hour
				options.MaxSize = 1
			}
			cleaner := buffer.NewCleaner(options)
			require.NoError(t, cleaner.CleanupNow(ctx))
			require.NoError(t, p.ValidateBoundary(ctx, current))
			count, err := b.Count()
			require.NoError(t, err)
			require.Equal(t, 3, count)
			oldMarker, err := cursor.DecodeProgressMarker(current)
			require.NoError(t, err)
			oldMarker.Positions["source"] = old.EventID
			require.ErrorContains(t, p.ValidateBoundary(ctx, oldMarker.Encode()), "expired")
			require.ErrorContains(t, p.ValidateBoundary(ctx, initial), "expired")
			replay, err := p.ReplayBoundary(ctx, current, false)
			require.NoError(t, err)
			var replayed []string
			for replay.Next() {
				replayed = append(replayed, replay.Event().EventID)
			}
			require.NoError(t, replay.Err())
			require.NoError(t, replay.Close())
			require.Equal(t, []string{idleRetentionEvent(latestTime, "a").EventID, idleRetentionEvent(latestTime, "b").EventID, idleRetentionEvent(latestTime, "c").EventID}, replayed)
			tokenAfterCleanup, err := b.LoadCheckpoint()
			require.NoError(t, err)
			require.Equal(t, lastToken, tokenAfterCleanup)

			next := idleRetentionEvent(latestTime, "d")
			write(next)
			p.subs.Broadcast(next)
			delivered := idleRetentionReceive(t, ctx, first)
			require.Equal(t, next.EventID, delivered.Change.EventID)
			current = delivered.Progress
			require.NoError(t, b.Flush(ctx))
			require.NoError(t, cleaner.CleanupNow(ctx))
			require.NoError(t, p.ValidateBoundary(ctx, current))
			stopFirst()
			for evt := range first {
				t.Errorf("unexpected event after idle subscription cancellation: %+v", evt)
			}
			require.Zero(t, p.subs.Count())
			require.NoError(t, b.Close())
			b, err = buffer.New(buffer.Options{Path: path, Logger: logger})
			require.NoError(t, err)
			require.Equal(t, lineage, b.Lineage())
			restored, err := b.LoadCheckpoint()
			require.NoError(t, err)
			require.Equal(t, lastToken, restored)
			p = New(config.DefaultConfig(), logger)
			backend = &Backend{name: "source", buffer: b}
			backend.setCaptureState(true, nil)
			p.backends[backend.name] = backend
			require.NoError(t, p.ValidateBoundary(ctx, current))
			second := p.SubscribeReady(ctx, "indexer", current, nil)
			for _, suffix := range []string{"a", "b", "c", "d"} {
				evt := idleRetentionReceive(t, ctx, second)
				require.Equal(t, idleRetentionEvent(latestTime, suffix).EventID, evt.Change.EventID)
			}
			require.True(t, idleRetentionReceive(t, ctx, second).Ready)
			fresh := idleRetentionEvent(latestTime+1, "fresh")
			write(fresh)
			p.subs.Broadcast(fresh)
			require.Equal(t, fresh.EventID, idleRetentionReceive(t, ctx, second).Change.EventID)
			cancel()
			for evt := range second {
				t.Errorf("unexpected event after reopened subscription cancellation: %+v", evt)
			}
			require.Zero(t, p.subs.Count())
		})
	}
}

func TestIdleRetentionDoesNotRepairExplicitlyIncompleteTimestampGroups(t *testing.T) {
	t.Parallel()
	for _, deletion := range []string{"single", "before"} {
		t.Run(deletion, func(t *testing.T) {
			t.Parallel()
			p, backend, initial := nativeBoundaryFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			for _, suffix := range []string{"a", "b", "c"} {
				require.NoError(t, backend.buffer.Write(ctx, idleRetentionEvent(1, suffix), bson.Raw{5, 0, 0, 0, 0}))
			}
			require.NoError(t, backend.buffer.Flush(ctx))
			marker, err := cursor.DecodeProgressMarker(initial)
			require.NoError(t, err)
			marker.Positions["source"] = idleRetentionEvent(1, "c").EventID
			if deletion == "single" {
				require.NoError(t, backend.buffer.Delete(idleRetentionEvent(1, "a").BufferKey()))
			} else {
				deleted, err := backend.buffer.DeleteBefore(idleRetentionEvent(1, "b").BufferKey())
				require.NoError(t, err)
				require.Equal(t, 1, deleted)
			}
			cleaner := buffer.NewCleaner(buffer.CleanerOptions{Buffer: backend.buffer, Retention: time.Minute, Logger: p.logger})
			require.NoError(t, cleaner.CleanupNow(ctx))
			require.ErrorContains(t, p.ValidateBoundary(ctx, marker.Encode()), "expired")
			iter, err := p.ReplayBoundary(ctx, marker.Encode(), false)
			require.Error(t, err)
			require.Nil(t, iter)
		})
	}
}

func TestIdleRetentionKeepsEmptyNativeBoundaryReady(t *testing.T) {
	t.Parallel()
	p, backend, marker := nativeBoundaryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cleaner := buffer.NewCleaner(buffer.CleanerOptions{Buffer: backend.buffer, Retention: time.Minute, MaxSize: 1, Logger: p.logger})
	require.NoError(t, cleaner.CleanupNow(ctx))
	require.NoError(t, p.ValidateBoundary(ctx, marker))
	stream := p.SubscribeReady(ctx, "indexer", marker, nil)
	ready := idleRetentionReceive(t, ctx, stream)
	require.True(t, ready.Ready)
	require.Equal(t, marker, ready.Progress)
	cancel()
	for evt := range stream {
		t.Errorf("unexpected event from empty source: %+v", evt)
	}
}

func TestAgeRetentionKeepsConsumedBoundaryDuringPersistedLiveHandoff(t *testing.T) {
	t.Parallel()
	p, backend, initial := nativeBoundaryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	freshTime := uint32(time.Now().Unix())
	consumed := idleRetentionEvent(freshTime-7200, "consumed")
	older := idleRetentionEvent(freshTime-10800, "older")
	for _, evt := range []*events.StoreChangeEvent{older, consumed} {
		require.NoError(t, backend.buffer.Write(ctx, evt, bson.Raw{5, 0, 0, 0, 0}))
	}
	require.NoError(t, backend.buffer.Flush(ctx))
	stream := p.SubscribeReady(ctx, "indexer", initial, nil)
	for _, evt := range []*events.StoreChangeEvent{older, consumed} {
		require.Equal(t, evt.EventID, idleRetentionReceive(t, ctx, stream).Change.EventID)
	}
	ready := idleRetentionReceive(t, ctx, stream)
	require.True(t, ready.Ready)
	consumedBoundary := ready.Progress
	fresh := idleRetentionEvent(freshTime, "persisted-before-broadcast")
	require.NoError(t, backend.buffer.Write(ctx, fresh, bson.Raw{5, 0, 0, 0, 0}))
	require.NoError(t, backend.buffer.Flush(ctx))
	cleaner := buffer.NewCleaner(buffer.CleanerOptions{Buffer: backend.buffer, Retention: time.Hour, Logger: p.logger})
	require.NoError(t, cleaner.CleanupNow(ctx))
	require.NoError(t, p.ValidateBoundary(ctx, consumedBoundary))
	count, err := backend.buffer.Count()
	require.NoError(t, err)
	require.Equal(t, 2, count)
	marker, err := cursor.DecodeProgressMarker(consumedBoundary)
	require.NoError(t, err)
	marker.Positions["source"] = older.EventID
	require.ErrorContains(t, p.ValidateBoundary(ctx, marker.Encode()), "expired")

	p.subs.Broadcast(fresh)
	live := idleRetentionReceive(t, ctx, stream)
	require.Equal(t, fresh.EventID, live.Change.EventID)
	deleted, err := backend.buffer.PruneExpired(events.FormatBufferKey(events.ClusterTime{T: freshTime + 1}, ""))
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	require.ErrorContains(t, p.ValidateBoundary(ctx, consumedBoundary), "expired")
	require.NoError(t, p.ValidateBoundary(ctx, live.Progress))
	cancel()
	for evt := range stream {
		t.Errorf("unexpected event after completed live handoff: %+v", evt)
	}
	require.Zero(t, p.subs.Count())
}
