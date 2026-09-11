package buffer

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

func TestBuffer_Revalidate(t *testing.T) {
	b := &Buffer{}
	assert.NoError(t, b.Revalidate(time.Second))
}

func TestSliceIterator_Coverage(t *testing.T) {
	// Setup checks for sliceIterator
	evts := []*events.StoreChangeEvent{
		{EventID: "1"},
		{EventID: "2"},
	}
	keys := []string{"k1", "k2"} // matching lengths normally

	it := &sliceIterator{
		events: evts,
		keys:   keys,
		index:  -1,
	}

	// Test Next/Event/Key sequence
	// Initial state
	assert.Nil(t, it.Event())
	assert.Equal(t, "", it.Key())

	// First
	assert.True(t, it.Next())
	assert.Equal(t, "1", it.Event().EventID)
	assert.Equal(t, "k1", it.Key())

	// Second
	assert.True(t, it.Next())
	assert.Equal(t, "2", it.Event().EventID)
	assert.Equal(t, "k2", it.Key())

	// End
	assert.False(t, it.Next())
	// When Next returns false, the iterator is exhausted.
	// We don't assert strict state of Event()/Key() here as implementation leaves them at last valid.

	assert.NoError(t, it.Err())
	assert.NoError(t, it.Close())
}

func TestSliceIterator_EdgeCases(t *testing.T) {
	// Mismatch length or empty
	it := &sliceIterator{
		events: []*events.StoreChangeEvent{},
		keys:   []string{"k1"}, // Mismatch
		index:  -1,
	}
	assert.False(t, it.Next())

	it2 := &sliceIterator{
		events: []*events.StoreChangeEvent{{}},
		keys:   []string{}, // Mismatch
		index:  -1,
	}
	it2.Next()
	// event exists, key missing
	assert.NotNil(t, it2.Event())
	assert.Equal(t, "", it2.Key())
}

func TestNewSnapshotIterator_Methods(t *testing.T) {
	b := &Buffer{pending: []*writeRequest{
		{key: []byte("k1"), event: &events.StoreChangeEvent{EventID: "e1"}},
		{key: []byte("k0")},
		{key: []byte("k2"), event: &events.StoreChangeEvent{EventID: "e2"}},
	}}
	iter := b.newSnapshotIterator("k1")
	assert.True(t, iter.Next())
	assert.Equal(t, "e2", iter.Event().EventID)
	assert.False(t, iter.Next())
}

func TestDeduplicatingIterator_Coverage(t *testing.T) {
	done := make(chan bool)
	go func() {
		// Create two slice iterators with overlapping keys
		it1 := &sliceIterator{
			events: []*events.StoreChangeEvent{{EventID: "1"}},
			keys:   []string{"100"},
			index:  -1,
		}
		it2 := &sliceIterator{
			events: []*events.StoreChangeEvent{{EventID: "2"}, {EventID: "3"}},
			keys:   []string{"100", "200"},
			index:  -1,
		}

		dedup := newDeduplicatingIterator(it1, it2)

		// 1. from it1: key=100
		assert.True(t, dedup.Next())
		assert.Equal(t, "1", dedup.Event().EventID)
		assert.Equal(t, "100", dedup.Key())

		// The duplicate key is emitted once across both sources.
		//    from it2: key=200 -> OK
		assert.True(t, dedup.Next())
		assert.Equal(t, "3", dedup.Event().EventID)
		assert.Equal(t, "200", dedup.Key())

		// 3. End
		assert.False(t, dedup.Next())
		assert.Nil(t, dedup.Event())
		assert.Equal(t, "", dedup.Key())
		done <- true
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("TestDeduplicatingIterator_Coverage timed out")
	}
}

type failingMergeIterator struct {
	*sliceIterator
	readError  error
	closeError error
	err        error
	closeCalls int
}

func (i *failingMergeIterator) Next() bool {
	if i.sliceIterator.Next() {
		return true
	}
	i.err = i.readError
	return false
}

func (i *failingMergeIterator) Err() error { return i.err }

func (i *failingMergeIterator) Close() error {
	i.closeCalls++
	return i.closeError
}

func TestMergedReplayStopsOnReadFailureAndClosesEverySource(t *testing.T) {
	t.Parallel()
	readErr := errors.New("unreadable queued event")
	firstCloseErr := errors.New("disk snapshot close failure")
	secondCloseErr := errors.New("queue snapshot close failure")
	first := &failingMergeIterator{
		sliceIterator: &sliceIterator{
			events: []*events.StoreChangeEvent{{EventID: "a"}, {EventID: "c"}},
			keys:   []string{"a", "c"}, index: -1,
		},
		closeError: firstCloseErr,
	}
	second := &failingMergeIterator{
		sliceIterator: &sliceIterator{
			events: []*events.StoreChangeEvent{{EventID: "b"}},
			keys:   []string{"b"}, index: -1,
		},
		readError: readErr, closeError: secondCloseErr,
	}
	merged := newDeduplicatingIterator(first, second)
	for _, key := range []string{"a", "b"} {
		require.True(t, merged.Next())
		require.Equal(t, key, merged.Key())
	}
	require.False(t, merged.Next())
	require.ErrorIs(t, merged.Err(), readErr)
	require.False(t, merged.Next())
	require.Nil(t, merged.Event())
	err := merged.Close()
	require.ErrorIs(t, err, firstCloseErr)
	require.ErrorIs(t, err, secondCloseErr)
	require.ErrorIs(t, merged.Err(), readErr)
	require.NoError(t, merged.Close())
	require.Equal(t, 1, first.closeCalls)
	require.Equal(t, 1, second.closeCalls)
}
