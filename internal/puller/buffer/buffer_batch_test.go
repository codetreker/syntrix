package buffer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

func TestBuffer_Write_CommitFailureStopsPendingBatch(t *testing.T) {
	t.Parallel()
	for _, closeDuringCommit := range []bool{false, true} {
		name := "background failure"
		if closeDuringCommit {
			name = "close during failure"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			buf, err := New(Options{Path: dir, BatchSize: 1, BatchInterval: time.Hour})
			require.NoError(t, err)

			token0, err := bson.Marshal(bson.M{"position": 0})
			require.NoError(t, err)
			require.NoError(t, buf.SaveCheckpoint(token0))
			token1, err := bson.Marshal(bson.M{"position": 1})
			require.NoError(t, err)
			token2, err := bson.Marshal(bson.M{"position": 2})
			require.NoError(t, err)

			commitStarted := make(chan struct{})
			releaseCommit := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(releaseCommit) })
				buf.Close()
			})
			batchErr := errors.New("first batch commit failed")
			batchCount := 0
			buf.newBatch = func() pebbleBatch {
				batchCount++
				if batchCount > 1 {
					return buf.db.NewBatch()
				}
				return &fakeBatch{commit: func() error {
					close(commitStarted)
					<-releaseCommit
					return batchErr
				}}
			}

			require.NoError(t, buf.Write(context.Background(), &events.StoreChangeEvent{EventID: "evt-1"}, token1))
			select {
			case <-commitStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("first batch did not reach commit")
			}
			require.NoError(t, buf.Write(context.Background(), &events.StoreChangeEvent{EventID: "evt-2"}, token2))

			closeResults := make(chan error, 2)
			if closeDuringCommit {
				for range 2 {
					go func() { closeResults <- buf.Close() }()
				}
				select {
				case <-buf.closeCh:
				case <-time.After(5 * time.Second):
					t.Fatal("Close did not signal the batcher")
				}
			}
			releaseOnce.Do(func() { close(releaseCommit) })
			batcherDone := make(chan struct{})
			go func() {
				buf.batcherWG.Wait()
				close(batcherDone)
			}()
			select {
			case <-batcherDone:
			case <-time.After(5 * time.Second):
				t.Fatal("batcher did not stop after commit failed")
			}

			assert.Equal(t, 1, batchCount)
			assert.ErrorIs(t, buf.Write(context.Background(), &events.StoreChangeEvent{EventID: "evt-3"}, token2), batchErr)
			assert.ErrorIs(t, buf.Flush(context.Background()), batchErr)
			_, err = buf.LoadCheckpoint()
			assert.ErrorIs(t, err, batchErr)
			if closeDuringCommit {
				for range 2 {
					select {
					case err := <-closeResults:
						assert.ErrorIs(t, err, batchErr)
					case <-time.After(5 * time.Second):
						t.Fatal("Close did not return after batch failure")
					}
				}
			}
			assert.ErrorIs(t, buf.Close(), batchErr)
			assert.ErrorIs(t, buf.Close(), batchErr)

			reopened, err := New(Options{Path: dir})
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
			checkpoint, err := reopened.LoadCheckpoint()
			require.NoError(t, err)
			assert.Equal(t, bson.Raw(token0), checkpoint)
			count, err := reopened.Count()
			require.NoError(t, err)
			assert.Zero(t, count)
		})
	}
}

func TestBuffer_Write_BatchErrorsRemainObservable(t *testing.T) {
	t.Parallel()
	batchErr := errors.New("batch failure")
	for name, mockBatch := range map[string]*fakeBatch{
		"event set":      {setErr: batchErr, setErrorAt: 1},
		"checkpoint set": {setErr: batchErr, setErrorAt: 2},
		"close":          {closeErr: batchErr},
		"first error":    {commitErr: batchErr, closeErr: errors.New("later close failure")},
	} {
		t.Run(name, func(t *testing.T) {
			buf, err := New(Options{Path: t.TempDir(), BatchSize: 1, BatchInterval: time.Hour})
			require.NoError(t, err)
			t.Cleanup(func() { buf.Close() })
			buf.newBatch = func() pebbleBatch { return mockBatch }
			require.NoError(t, buf.Write(context.Background(), &events.StoreChangeEvent{EventID: "evt-1"}, testToken))
			select {
			case <-buf.closeCh:
			case <-time.After(5 * time.Second):
				t.Fatal("batcher did not report failure")
			}
			assert.ErrorIs(t, buf.Close(), batchErr)
			assert.ErrorIs(t, buf.Close(), batchErr)
			assert.ErrorIs(t, buf.Write(context.Background(), &events.StoreChangeEvent{EventID: "evt-2"}, testToken), batchErr)
			_, err = buf.LoadCheckpoint()
			assert.ErrorIs(t, err, batchErr)
			assert.True(t, mockBatch.closed)
		})
	}
}

func TestBuffer_Delete_CommitErrorUsesBatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	buf, err := New(Options{Path: dir})
	require.NoError(t, err)
	defer buf.Close()

	batchErr := errors.New("commit failed")
	mockBatch := &fakeBatch{commitErr: batchErr}
	buf.newBatch = func() pebbleBatch {
		return mockBatch
	}

	err = buf.Delete("evt-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit")
	assert.Equal(t, 1, mockBatch.deleteCalls)
	assert.True(t, mockBatch.closed)
}

type fakeBatch struct {
	set         func(key, value []byte) error
	close       func() error
	setCalls    int
	deleteCalls int
	setErr      error
	setErrorAt  int
	deleteErr   error
	commitErr   error
	commit      func() error
	closeErr    error
	closed      bool
}

func (f *fakeBatch) Set(key, value []byte, opts *pebble.WriteOptions) error {
	f.setCalls++
	if f.set != nil {
		return f.set(key, value)
	}
	if f.setErrorAt == 0 || f.setCalls == f.setErrorAt {
		return f.setErr
	}
	return nil
}

func (f *fakeBatch) Delete(key []byte, opts *pebble.WriteOptions) error {
	f.deleteCalls++
	return f.deleteErr
}

func (f *fakeBatch) Commit(opts *pebble.WriteOptions) error {
	if f.commit != nil {
		return f.commit()
	}
	return f.commitErr
}

func (f *fakeBatch) Close() error {
	f.closed = true
	if f.close != nil {
		return f.close()
	}
	return f.closeErr
}

// waitContext exposes entry to the full-queue wait without timing assumptions.
type waitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

type cancelAfterEncodingContext struct {
	context.Context
	checks int
}

func (c *cancelAfterEncodingContext) Err() error {
	c.checks++
	if c.checks > 1 {
		return context.Canceled
	}
	return nil
}

func TestBuffer_Write_WaitsBeforeEncodingAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	buf, err := New(Options{Path: t.TempDir(), QueueSize: 1, BatchSize: 10, BatchInterval: time.Hour})
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); buf.Close() })
	buf.newBatch = func() pebbleBatch {
		return &fakeBatch{commit: func() error {
			close(started)
			<-release
			return nil
		}}
	}
	require.NoError(t, buf.Write(context.Background(), &events.StoreChangeEvent{EventID: "first"}, testToken))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("a queue smaller than batch size did not flush when full")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &waitContext{Context: ctx, waiting: make(chan struct{})}
	evt := &events.StoreChangeEvent{FullDocument: &storage.StoredDoc{Data: map[string]interface{}{
		"probe": make(chan int),
	}}}
	result := make(chan error, 1)
	go func() { result <- buf.Write(observed, evt, testToken) }()
	select {
	case <-observed.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("write did not wait for capacity")
	}
	buf.mu.RLock()
	assert.Len(t, buf.pending, 0)
	assert.Len(t, buf.flushing, 1)
	buf.mu.RUnlock()

	other, err := New(Options{Path: t.TempDir(), QueueSize: 1, BatchSize: 1})
	require.NoError(t, err)
	t.Cleanup(func() { other.Close() })
	require.NoError(t, other.Write(context.Background(), &events.StoreChangeEvent{EventID: "independent"}, testToken))
	require.NoError(t, other.Close())

	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not release write")
	}
	releaseOnce.Do(func() { close(release) })
	require.NoError(t, buf.Close())
}

func TestBuffer_Write_CancellationDuringEncodingDoesNotAdmit(t *testing.T) {
	t.Parallel()
	buf, err := New(Options{Path: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })
	ctx := &cancelAfterEncodingContext{Context: context.Background()}
	evt := &events.StoreChangeEvent{EventID: "canceled"}
	require.ErrorIs(t, buf.Write(ctx, evt, testToken), context.Canceled)
	buf.mu.RLock()
	assert.Empty(t, buf.pending)
	assert.Empty(t, buf.flushing)
	buf.mu.RUnlock()
}

func TestBuffer_Write_FullQueueWaitersWake(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"commit", "failure", "close"} {
		t.Run(outcome, func(t *testing.T) {
			buf, err := New(Options{Path: t.TempDir(), QueueSize: 2, BatchSize: 1, BatchInterval: time.Hour})
			require.NoError(t, err)
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); buf.Close() })
			batchErr := errors.New("commit failed")
			first := true
			buf.newBatch = func() pebbleBatch {
				if !first {
					return &fakeBatch{}
				}
				first = false
				return &fakeBatch{commit: func() error {
					close(started)
					<-release
					if outcome == "failure" {
						return batchErr
					}
					return nil
				}}
			}
			require.NoError(t, buf.Write(context.Background(), &events.StoreChangeEvent{EventID: "first"}, testToken))
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("commit did not start")
			}
			require.NoError(t, buf.Write(context.Background(), &events.StoreChangeEvent{EventID: "second"}, testToken))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			results := make(chan error, 2)
			for range 2 {
				observed := &waitContext{Context: ctx, waiting: make(chan struct{})}
				go func() { results <- buf.Write(observed, &events.StoreChangeEvent{EventID: "waiting"}, testToken) }()
				select {
				case <-observed.waiting:
				case <-ctx.Done():
					t.Fatal("writer did not reach full queue")
				}
			}
			buf.mu.RLock()
			assert.Len(t, buf.pending, 1)
			assert.Len(t, buf.flushing, 1)
			buf.mu.RUnlock()
			closeResult := make(chan error, 1)
			if outcome == "close" {
				go func() { closeResult <- buf.Close() }()
				select {
				case <-buf.closeCh:
				case <-ctx.Done():
					t.Fatal("close did not signal")
				}
			} else {
				releaseOnce.Do(func() { close(release) })
			}
			for range 2 {
				select {
				case err := <-results:
					switch outcome {
					case "commit":
						require.NoError(t, err)
					case "failure":
						require.ErrorIs(t, err, batchErr)
					case "close":
						require.ErrorContains(t, err, "buffer is closed")
					}
				case <-ctx.Done():
					t.Fatal("capacity waiter was not released")
				}
			}
			releaseOnce.Do(func() { close(release) })
			if outcome == "close" {
				require.NoError(t, <-closeResult)
			}
			if outcome == "failure" {
				require.ErrorIs(t, buf.Close(), batchErr)
			} else {
				require.NoError(t, buf.Close())
			}
		})
	}
}

func TestBuffer_Write_CappedBatchesPreserveOrderAndCheckpoint(t *testing.T) {
	t.Parallel()
	for _, closing := range []bool{false, true} {
		t.Run(fmt.Sprintf("close=%v", closing), func(t *testing.T) {
			count := 8
			if closing {
				count = 7
			}
			dir := t.TempDir()
			buf, err := New(Options{Path: dir, QueueSize: count, BatchSize: 2, BatchInterval: time.Hour})
			require.NoError(t, err)
			started, release, drained := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); buf.Close() })
			var keys []string
			var batchSizes []int
			var tokens []bson.Raw
			batches := 0
			buf.newBatch = func() pebbleBatch {
				batches++
				index := batches
				real := buf.db.NewBatch()
				size := 0
				return &fakeBatch{
					set: func(key, value []byte) error {
						if string(key) == checkpointKey {
							tokens = append(tokens, append(bson.Raw(nil), value...))
						} else {
							keys = append(keys, string(key))
							size++
						}
						return real.Set(key, value, pebble.Sync)
					},
					commit: func() error {
						if index == 1 {
							close(started)
							<-release
						}
						batchSizes = append(batchSizes, size)
						err := real.Commit(pebble.Sync)
						if len(keys) == count {
							close(drained)
						}
						return err
					},
					close: real.Close,
				}
			}
			var expectedKeys []string
			var allTokens []bson.Raw
			for i := range count {
				token, err := bson.Marshal(bson.M{"position": i + 1})
				require.NoError(t, err)
				evt := &events.StoreChangeEvent{EventID: fmt.Sprintf("event-%d", i+1)}
				require.NoError(t, buf.Write(context.Background(), evt, token))
				expectedKeys = append(expectedKeys, evt.BufferKey())
				allTokens = append(allTokens, token)
				if i == 1 {
					select {
					case <-started:
					case <-time.After(5 * time.Second):
						t.Fatal("first commit did not start")
					}
				}
			}
			snapshot := buf.newSnapshotIterator("")
			defer snapshot.Close()
			closeResult := make(chan error, 1)
			if closing {
				go func() { closeResult <- buf.Close() }()
				select {
				case <-buf.closeCh:
				case <-time.After(5 * time.Second):
					t.Fatal("close did not start")
				}
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case <-drained:
			case <-time.After(5 * time.Second):
				t.Fatal("queued batches required a fresh notification")
			}
			if closing {
				require.NoError(t, <-closeResult)
			}
			require.NoError(t, buf.Close())
			assert.Equal(t, expectedKeys, keys)
			expectedSizes := []int{2, 2, 2, 2}
			if closing {
				expectedSizes[3] = 1
			}
			assert.Equal(t, expectedSizes, batchSizes)
			assert.Equal(t, []bson.Raw{allTokens[1], allTokens[3], allTokens[5], allTokens[count-1]}, tokens)
			var snapshotKeys []string
			for snapshot.Next() {
				snapshotKeys = append(snapshotKeys, snapshot.Event().BufferKey())
			}
			assert.Equal(t, expectedKeys, snapshotKeys)
			reopened, err := New(Options{Path: dir})
			require.NoError(t, err)
			t.Cleanup(func() { reopened.Close() })
			cp, err := reopened.LoadCheckpoint()
			require.NoError(t, err)
			assert.Equal(t, allTokens[count-1], cp)
			for _, key := range expectedKeys {
				evt, err := reopened.Read(key)
				require.NoError(t, err)
				require.NotNil(t, evt)
			}
		})
	}
}
