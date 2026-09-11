package buffer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

func TestScheduledRetentionPreservesAgedTimestampTail(t *testing.T) {
	t.Parallel()
	for _, maxSize := range []int64{0, 1} {
		name := "age"
		if maxSize > 0 {
			name = "age and size"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			dir := t.TempDir()
			b, err := New(Options{Path: dir, BatchInterval: time.Hour})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, b.Close()) })
			old := &events.StoreChangeEvent{EventID: "old", ClusterTime: events.ClusterTime{T: 1, I: 1}}
			a := &events.StoreChangeEvent{EventID: "a", ClusterTime: events.ClusterTime{T: 2, I: 1}}
			z := &events.StoreChangeEvent{EventID: "z", ClusterTime: a.ClusterTime}
			for _, evt := range []*events.StoreChangeEvent{old, z, a} {
				require.NoError(t, b.Write(ctx, evt, testToken))
			}
			require.NoError(t, b.Flush(ctx))
			cleaner := NewCleaner(CleanerOptions{Buffer: b, Retention: time.Second, MaxSize: maxSize})
			for range 2 {
				result := make(chan error, 1)
				go func() { result <- cleaner.CleanupNow(ctx) }()
				select {
				case err := <-result:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal("retention did not stop at the protected tail")
				}
			}
			require.ErrorContains(t, b.ValidatePosition(old.BufferKey(), b.Lineage()), "expired")
			require.NoError(t, b.Close())
			b, err = New(Options{Path: dir})
			require.NoError(t, err)
			iter, err := b.ScanFromLineage(z.BufferKey(), b.Lineage())
			require.NoError(t, err)
			var keys []string
			for iter.Next() {
				keys = append(keys, iter.Key())
			}
			require.NoError(t, iter.Err())
			require.NoError(t, iter.Close())
			require.Equal(t, []string{a.BufferKey(), z.BufferKey()}, keys)
			checkpoint, err := b.LoadCheckpoint()
			require.NoError(t, err)
			require.Equal(t, testToken, checkpoint)
			require.NoError(t, b.Delete(a.BufferKey()))
			require.ErrorContains(t, b.ValidatePosition(z.BufferKey(), b.Lineage()), "expired")
		})
	}
}

func TestRetentionPromotesOnlyDurableNewTimestampGroup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := New(Options{Path: t.TempDir(), BatchInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	cutoff := events.FormatBufferKey(events.ClusterTime{T: 99}, "")
	tail := &events.StoreChangeEvent{EventID: "z", ClusterTime: events.ClusterTime{T: 2, I: 1}}
	require.NoError(t, b.Write(ctx, tail, testToken))
	require.NoError(t, b.Flush(ctx))
	lateMember := &events.StoreChangeEvent{EventID: "a", ClusterTime: tail.ClusterTime}
	require.NoError(t, b.Write(ctx, lateMember, testToken))
	deleted, err := b.PruneBefore(cutoff)
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.NoError(t, b.ValidatePosition(tail.BufferKey(), b.Lineage()))
	require.NoError(t, b.Flush(ctx))
	deleted, err = b.PruneBefore(cutoff)
	require.NoError(t, err)
	require.Zero(t, deleted)

	newTail := &events.StoreChangeEvent{EventID: "next", ClusterTime: events.ClusterTime{T: tail.ClusterTime.T, I: tail.ClusterTime.I + 1}}
	require.NoError(t, b.Write(ctx, newTail, testToken))
	deleted, err = b.PruneBefore(cutoff)
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.NoError(t, b.ValidatePosition(tail.BufferKey(), b.Lineage()))
	require.NoError(t, b.Flush(ctx))
	deleted, err = b.PruneBefore(cutoff)
	require.NoError(t, err)
	require.Equal(t, 2, deleted)
	require.ErrorContains(t, b.ValidatePosition(tail.BufferKey(), b.Lineage()), "expired")
	require.NoError(t, b.ValidatePosition(newTail.BufferKey(), b.Lineage()))
}

type retentionCommitBatch struct {
	pebbleBatch
	commit func() error
}

func (b *retentionCommitBatch) Commit(*pebble.WriteOptions) error { return b.commit() }

func TestRetentionKeepsSelectedTailWhenNewGroupCommitsConcurrently(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := New(Options{Path: t.TempDir(), BatchInterval: time.Hour})
	require.NoError(t, err)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		require.NoError(t, b.Close())
	})
	old := &events.StoreChangeEvent{EventID: "old", ClusterTime: events.ClusterTime{T: 1}}
	tail := &events.StoreChangeEvent{EventID: "tail", ClusterTime: events.ClusterTime{T: 2}}
	for _, evt := range []*events.StoreChangeEvent{old, tail} {
		require.NoError(t, b.Write(ctx, evt, testToken))
	}
	require.NoError(t, b.Flush(ctx))
	var batches atomic.Int32
	b.newBatch = func() pebbleBatch {
		real := b.db.NewBatch()
		if batches.Add(1) != 1 {
			return real
		}
		return &retentionCommitBatch{pebbleBatch: real, commit: func() error {
			close(started)
			<-release
			return real.Commit(pebble.Sync)
		}}
	}
	type pruneResult struct {
		count int
		err   error
	}
	result := make(chan pruneResult, 1)
	cutoff := events.FormatBufferKey(events.ClusterTime{T: 99}, "")
	go func() {
		count, err := b.PruneBefore(cutoff)
		result <- pruneResult{count, err}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("pruning did not reach its commit")
	}
	newTail := &events.StoreChangeEvent{EventID: "new", ClusterTime: events.ClusterTime{T: 3}}
	flushResult := make(chan error, 1)
	go func() {
		if err := b.Write(ctx, newTail, testToken); err != nil {
			flushResult <- err
			return
		}
		flushResult <- b.Flush(ctx)
	}()
	select {
	case err := <-flushResult:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("capture could not flush while retention commit was blocked")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case result := <-result:
		require.NoError(t, result.err)
		require.Equal(t, 1, result.count)
	case <-ctx.Done():
		t.Fatal("pruning did not finish")
	}
	require.NoError(t, b.ValidatePosition(tail.BufferKey(), b.Lineage()))
	deleted, err := b.PruneBefore(cutoff)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	require.ErrorContains(t, b.ValidatePosition(tail.BufferKey(), b.Lineage()), "expired")
	require.NoError(t, b.ValidatePosition(newTail.BufferKey(), b.Lineage()))
}

func TestRetentionLeavesEmptyAndPendingOnlyBuffersUnpruned(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := New(Options{Path: t.TempDir(), BatchInterval: time.Hour})
	require.NoError(t, err)
	defer b.Close()
	cutoff := events.FormatBufferKey(events.ClusterTime{T: 99}, "")
	deleted, err := b.PruneBefore(cutoff)
	require.NoError(t, err)
	require.Zero(t, deleted)
	pending := &events.StoreChangeEvent{EventID: "pending", ClusterTime: events.ClusterTime{T: 1}}
	require.NoError(t, b.Write(ctx, pending, testToken))
	deleted, err = b.PruneBefore(cutoff)
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.NoError(t, b.ValidatePosition("", b.Lineage()))
	require.NoError(t, b.ValidatePosition(pending.BufferKey(), b.Lineage()))
	require.NoError(t, b.Close())
	_, err = b.PruneBefore(cutoff)
	require.ErrorContains(t, err, "buffer is closed")
}

func TestRetentionFailureDoesNotAdvancePruningFloor(t *testing.T) {
	t.Parallel()
	for _, corruptHead := range []bool{false, true} {
		name := "commit failure"
		if corruptHead {
			name = "malformed durable head"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			b, err := New(Options{Path: t.TempDir(), BatchInterval: time.Hour})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, b.Close()) })
			old := &events.StoreChangeEvent{EventID: "old", ClusterTime: events.ClusterTime{T: 1}}
			tail := &events.StoreChangeEvent{EventID: "tail", ClusterTime: events.ClusterTime{T: 2}}
			for _, evt := range []*events.StoreChangeEvent{old, tail} {
				require.NoError(t, b.Write(ctx, evt, testToken))
			}
			require.NoError(t, b.Flush(ctx))
			commitErr := errors.New("retention commit failed")
			if corruptHead {
				require.NoError(t, b.db.Set([]byte("z-malformed-head"), []byte("invalid"), pebble.Sync))
			} else {
				b.newBatch = func() pebbleBatch {
					return &retentionCommitBatch{pebbleBatch: b.db.NewBatch(), commit: func() error { return commitErr }}
				}
			}
			for range 2 {
				deleted, err := b.PruneBefore(events.FormatBufferKey(events.ClusterTime{T: 99}, ""))
				require.Zero(t, deleted)
				if corruptHead {
					require.ErrorContains(t, err, "invalid event position")
				} else {
					require.ErrorIs(t, err, commitErr)
				}
				require.NoError(t, b.ValidatePosition("", b.Lineage()))
				require.NoError(t, b.ValidatePosition(old.BufferKey(), b.Lineage()))
				require.NoError(t, b.ValidatePosition(tail.BufferKey(), b.Lineage()))
				count, err := b.Count()
				require.NoError(t, err)
				wantCount := 2
				if corruptHead {
					wantCount++
				}
				require.Equal(t, wantCount, count)
			}
		})
	}
}

func TestCapacityRetentionCrossesLargeOlderGroupAndStopsAtTail(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := New(Options{Path: t.TempDir(), BatchInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	now := uint32(time.Now().Unix())
	oldTime := events.ClusterTime{T: now - 100, I: 1}
	for n := range 1205 {
		evt := &events.StoreChangeEvent{EventID: fmt.Sprintf("old-%04d", n), ClusterTime: oldTime}
		require.NoError(t, b.Write(ctx, evt, testToken))
	}
	tailTime := events.ClusterTime{T: now - 10, I: 1}
	for _, id := range []string{"a", "z"} {
		require.NoError(t, b.Write(ctx, &events.StoreChangeEvent{EventID: id, ClusterTime: tailTime}, testToken))
	}
	require.NoError(t, b.Flush(ctx))
	cleaner := NewCleaner(CleanerOptions{Buffer: b, Retention: 24 * time.Hour, MaxSize: 1})
	require.NoError(t, cleaner.CleanupNow(ctx))
	count, err := b.Count()
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.ErrorContains(t, b.ValidatePosition(events.FormatBufferKey(oldTime, "old-1204"), b.Lineage()), "expired")
	for _, id := range []string{"a", "z"} {
		require.NoError(t, b.ValidatePosition(events.FormatBufferKey(tailTime, id), b.Lineage()))
	}
}

func TestAgeRetentionKeepsCutoffPredecessorWhileNewGroupArrives(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := New(Options{Path: t.TempDir(), BatchInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	old := &events.StoreChangeEvent{EventID: "old", ClusterTime: events.ClusterTime{T: 1}}
	a := &events.StoreChangeEvent{EventID: "a", ClusterTime: events.ClusterTime{T: 2, I: 7}}
	z := &events.StoreChangeEvent{EventID: "z", ClusterTime: a.ClusterTime}
	newTail := &events.StoreChangeEvent{EventID: "new", ClusterTime: events.ClusterTime{T: 4}}
	for _, evt := range []*events.StoreChangeEvent{old, z, a, newTail} {
		require.NoError(t, b.Write(ctx, evt, testToken))
	}
	require.NoError(t, b.Flush(ctx))
	deleted, err := b.PruneExpired(events.FormatBufferKey(events.ClusterTime{T: 3}, ""))
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	for _, evt := range []*events.StoreChangeEvent{a, z, newTail} {
		require.NoError(t, b.ValidatePosition(evt.BufferKey(), b.Lineage()))
	}
	require.ErrorContains(t, b.ValidatePosition(old.BufferKey(), b.Lineage()), "expired")
	deleted, err = b.PruneExpired(events.FormatBufferKey(events.ClusterTime{T: 5}, ""))
	require.NoError(t, err)
	require.Equal(t, 2, deleted)
	require.ErrorContains(t, b.ValidatePosition(z.BufferKey(), b.Lineage()), "expired")
	require.NoError(t, b.ValidatePosition(newTail.BufferKey(), b.Lineage()))
}

func TestRetentionCleanerStopsBeforeDeletingOnCancellationOrCorruptScan(t *testing.T) {
	t.Parallel()
	for _, corrupt := range []bool{false, true} {
		name := "canceled cleanup"
		if corrupt {
			name = "corrupt capacity scan"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			b, err := New(Options{Path: t.TempDir(), BatchInterval: time.Hour})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, b.Close()) })
			oldTime := events.ClusterTime{T: uint32(time.Now().Unix()) - 100}
			old := &events.StoreChangeEvent{EventID: "a", ClusterTime: oldTime}
			tail := &events.StoreChangeEvent{EventID: "tail", ClusterTime: events.ClusterTime{T: oldTime.T + 1}}
			for _, evt := range []*events.StoreChangeEvent{old, tail} {
				require.NoError(t, b.Write(ctx, evt, testToken))
			}
			require.NoError(t, b.Flush(ctx))
			wantCount := 2
			if corrupt {
				require.NoError(t, b.db.Set([]byte(events.FormatBufferKey(oldTime, "m")), []byte("corrupt event"), pebble.Sync))
				wantCount++
			} else {
				cancel()
			}
			cleaner := NewCleaner(CleanerOptions{Buffer: b, Retention: 24 * time.Hour, MaxSize: 1})
			err = cleaner.CleanupNow(ctx)
			if corrupt {
				require.ErrorContains(t, err, "scan eviction candidates")
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
			count, err := b.Count()
			require.NoError(t, err)
			require.Equal(t, wantCount, count)
			require.NoError(t, b.ValidatePosition("", b.Lineage()))
			require.NoError(t, b.ValidatePosition(old.BufferKey(), b.Lineage()))
		})
	}
}
