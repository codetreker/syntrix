package buffer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

func TestNewCleaner(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "cleaner-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	buf, err := New(Options{Path: dir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer buf.Close()

	cleaner := NewCleaner(CleanerOptions{
		Buffer:    buf,
		Retention: time.Hour,
		Interval:  time.Minute,
		Logger:    nil,
	})

	if cleaner == nil {
		t.Fatal("NewCleaner() returned nil")
	}
	if cleaner.buffer != buf {
		t.Error("buffer should be set")
	}
	if cleaner.retention != time.Hour {
		t.Errorf("retention = %v, want 1h", cleaner.retention)
	}
	if cleaner.interval != time.Minute {
		t.Errorf("interval = %v, want 1m", cleaner.interval)
	}
}

func TestCleaner_StartAndStop(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "cleaner-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	buf, err := New(Options{Path: dir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer buf.Close()

	cleaner := NewCleaner(CleanerOptions{
		Buffer:    buf,
		Retention: time.Hour,
		Interval:  100 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cleaner.Start(ctx)

	// Let it run briefly
	time.Sleep(150 * time.Millisecond)

	// Stop should not block
	done := make(chan struct{})
	go func() {
		cleaner.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Expected
	case <-time.After(2 * time.Second):
		t.Error("Stop() took too long")
	}
}

func TestCleaner_CleanupNow(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	buf, err := New(Options{Path: t.TempDir(), BatchInterval: time.Hour})
	require.NoError(t, err)
	defer buf.Close()
	older := &events.StoreChangeEvent{EventID: "1-1-old", ClusterTime: events.ClusterTime{T: 1, I: 1}}
	predecessor := &events.StoreChangeEvent{EventID: "2-1-predecessor", ClusterTime: events.ClusterTime{T: 2, I: 1}}
	recent := &events.StoreChangeEvent{EventID: "recent", ClusterTime: events.ClusterTime{T: uint32(time.Now().Unix()), I: 1}}
	for _, event := range []*events.StoreChangeEvent{older, predecessor, recent} {
		require.NoError(t, buf.Write(ctx, event, testToken))
	}
	require.NoError(t, buf.Flush(ctx))
	cleaner := NewCleaner(CleanerOptions{Buffer: buf, Retention: time.Second, Interval: time.Hour})
	require.NoError(t, cleaner.CleanupNow(ctx))
	count, err := buf.Count()
	require.NoError(t, err)
	require.Equal(t, 2, count)
	removed, err := buf.Read(older.BufferKey())
	require.NoError(t, err)
	require.Nil(t, removed)
	require.NoError(t, buf.ValidatePosition(predecessor.BufferKey(), buf.Lineage()))
	require.NoError(t, buf.ValidatePosition(recent.BufferKey(), buf.Lineage()))
}

func TestCleaner_ContextCancellation(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "cleaner-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	buf, err := New(Options{Path: dir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer buf.Close()

	cleaner := NewCleaner(CleanerOptions{
		Buffer:    buf,
		Retention: time.Hour,
		Interval:  time.Hour, // Long interval so it doesn't trigger
	})

	ctx, cancel := context.WithCancel(context.Background())
	cleaner.Start(ctx)

	// Cancel context - cleaner should stop
	cancel()

	// Wait a bit and then stop to ensure no deadlock
	time.Sleep(150 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		cleaner.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Expected
	case <-time.After(time.Second):
		t.Error("Stop() took too long after context cancellation")
	}
}

func TestCleaner_RunTriggersCleanup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	buf, err := New(Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer buf.Close()
	old := &events.StoreChangeEvent{EventID: "1-1-old", ClusterTime: events.ClusterTime{T: 1, I: 1}}
	tail := &events.StoreChangeEvent{EventID: "2-1-tail", ClusterTime: events.ClusterTime{T: 2, I: 1}}
	require.NoError(t, buf.Write(ctx, old, testToken))
	require.NoError(t, buf.Write(ctx, tail, testToken))
	require.NoError(t, buf.Flush(ctx))
	cleaner := NewCleaner(CleanerOptions{Buffer: buf, Retention: time.Millisecond, Interval: 10 * time.Millisecond})
	cleaner.Start(ctx)
	defer cleaner.Stop()
	require.Eventually(t, func() bool { value, err := buf.Read(old.BufferKey()); return err == nil && value == nil }, time.Second, time.Millisecond)
	require.NoError(t, buf.ValidatePosition(tail.BufferKey(), buf.Lineage()))
	count, err := buf.Count()
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestCleaner_CleanupError(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "cleaner-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	buf, err := New(Options{Path: dir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	cleaner := NewCleaner(CleanerOptions{
		Buffer:    buf,
		Retention: time.Millisecond,
		Interval:  150 * time.Millisecond,
	})

	// Close buffer before cleanup runs - this will cause cleanup to fail
	buf.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cleaner.Start(ctx)

	// Wait for cleanup to be attempted (and fail)
	time.Sleep(100 * time.Millisecond)

	// Stop should still work even after cleanup errors
	done := make(chan struct{})
	go func() {
		cleaner.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Expected - should not hang
	case <-time.After(time.Second):
		t.Error("Stop() took too long after cleanup error")
	}
}

func TestCleaner_MaxSize(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "cleaner-test-maxsize-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	buf, err := New(Options{
		Path:          dir,
		BatchInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer buf.Close()

	// Write 10 events
	for i := 0; i < 10; i++ {
		evt := &events.StoreChangeEvent{
			EventID:  "evt-" + string(rune('a'+i)),
			MgoColl:  "testcoll",
			MgoDocID: "doc-1",
			OpType:   events.StoreOperationInsert,
			ClusterTime: events.ClusterTime{
				T: uint32(time.Now().Unix()) + uint32(i),
				I: 1,
			},
			// Add some payload to increase size
			FullDocument: &storage.StoredDoc{
				Id:       storage.CalculateDatabase("database-1", "testcoll/doc-1"),
				Database: "database-1", Collection: "testcoll", Fullpath: "testcoll/doc-1",
				Data: map[string]any{"data": "some payload"},
			},
		}
		if err := buf.Write(context.Background(), evt, testToken); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}

	// Wait for flush
	time.Sleep(50 * time.Millisecond)

	// Wait for size to be > 0
	assert.Eventually(t, func() bool {
		s, _ := buf.Size()
		return s > 0
	}, 5*time.Second, 100*time.Millisecond, "Buffer size should be > 0")

	initialCount, err := buf.Count()
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if initialCount != 10 {
		t.Fatalf("Initial count = %d, want 10", initialCount)
	}

	// Create cleaner with MaxSize = 1 (force eviction)
	cleaner := NewCleaner(CleanerOptions{
		Buffer:    buf,
		Retention: time.Hour,
		MaxSize:   1, // Force eviction
		Interval:  time.Hour,
	})

	// Run cleanup
	ctx := context.Background()
	err = cleaner.CleanupNow(ctx)
	if err != nil {
		t.Fatalf("CleanupNow() error = %v", err)
	}

	// Should have evicted events
	finalCount, err := buf.Count()
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}

	// The newest durable timestamp group is retained even above the soft limit.
	if finalCount != 1 {
		t.Errorf("Final count = %d, want 1 retained tail event", finalCount)
	}

}

func TestCleaner_StopBeforeContextCancel(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "cleaner-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	buf, err := New(Options{Path: dir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer buf.Close()

	cleaner := NewCleaner(CleanerOptions{
		Buffer:    buf,
		Retention: time.Hour,
		Interval:  10 * time.Second, // Long interval so ticker doesn't fire
	})

	ctx := context.Background() // Never cancelled

	cleaner.Start(ctx)

	// Wait briefly for the cleaner goroutine to start
	time.Sleep(50 * time.Millisecond)

	// Stop the cleaner - this should trigger <-c.done branch
	done := make(chan struct{})
	go func() {
		cleaner.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Good - cleaner stopped via done channel
	case <-time.After(2 * time.Second):
		t.Error("Stop() took too long - done channel may not be working")
	}
}

func TestEmptyBufferSizePressureKeepsCaptureMetadataAndTerminates(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	buf, err := New(Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer buf.Close()
	require.NoError(t, buf.SaveCheckpoint(testToken))
	lineage := buf.Lineage()
	size, err := buf.Size()
	require.NoError(t, err)
	require.Greater(t, size, int64(1))
	cleaner := NewCleaner(CleanerOptions{Buffer: buf, Retention: time.Hour, MaxSize: 1, Interval: time.Hour})
	require.NoError(t, cleaner.CleanupNow(ctx))
	count, err := buf.Count()
	require.NoError(t, err)
	require.Zero(t, count)
	checkpoint, err := buf.LoadCheckpoint()
	require.NoError(t, err)
	require.Equal(t, testToken, checkpoint)
	require.Equal(t, lineage, buf.Lineage())
	require.NoError(t, buf.ValidatePosition("", lineage))
}
