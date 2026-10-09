package core

import (
	"errors"
	"testing"

	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/stretchr/testify/assert"
)

// MockIterator is a simple mock for events.Iterator
type MockIterator struct {
	events []*events.StoreChangeEvent
	index  int
	err    error
}

func NewMockIterator(evts []*events.StoreChangeEvent) *MockIterator {
	return &MockIterator{
		events: evts,
		index:  -1,
	}
}

func (m *MockIterator) Next() bool {
	if m.err != nil {
		return false
	}
	m.index++
	return m.index < len(m.events)
}

func (m *MockIterator) Event() *events.StoreChangeEvent {
	if m.index >= 0 && m.index < len(m.events) {
		return m.events[m.index]
	}
	return nil
}

func (m *MockIterator) Err() error {
	return m.err
}

func (m *MockIterator) Close() error {
	return nil
}

func TestMergeIterator_Order(t *testing.T) {
	// Iterator 1: A(100), C(300)
	iter1 := NewMockIterator([]*events.StoreChangeEvent{
		{EventID: "A", ClusterTime: events.ClusterTime{T: 100}},
		{EventID: "C", ClusterTime: events.ClusterTime{T: 300}},
	})

	// Iterator 2: B(200), D(400)
	iter2 := NewMockIterator([]*events.StoreChangeEvent{
		{EventID: "B", ClusterTime: events.ClusterTime{T: 200}},
		{EventID: "D", ClusterTime: events.ClusterTime{T: 400}},
	})

	mi := NewMergeIterator([]events.Iterator{iter1, iter2})
	defer mi.Close()

	// Expect A, B, C, D
	expected := []string{"A", "B", "C", "D"}
	for _, id := range expected {
		if !mi.Next() {
			t.Fatalf("Expected event %s, got end of stream", id)
		}
		assert.Equal(t, id, mi.Event().EventID)
	}

	assert.False(t, mi.Next())
	assert.NoError(t, mi.Err())
}

func TestMergeIterator_Empty(t *testing.T) {
	iter1 := NewMockIterator(nil)
	iter2 := NewMockIterator(nil)

	mi := NewMergeIterator([]events.Iterator{iter1, iter2})
	defer mi.Close()

	assert.False(t, mi.Next())
	assert.Nil(t, mi.Event())
}

func TestMergeIterator_Error(t *testing.T) {
	iter1 := NewMockIterator([]*events.StoreChangeEvent{
		{EventID: "A", ClusterTime: events.ClusterTime{T: 100}},
	})
	iter2 := NewMockIterator(nil)
	iter2.err = errors.New("iter2 error")

	mi := NewMergeIterator([]events.Iterator{iter1, iter2})
	defer mi.Close()

	// Initialization might fail or first Next might fail depending on implementation
	// In current impl, NewMergeIterator checks Next(). If iter2 fails immediately,
	// mi.err should be set.

	if mi.Err() == nil {
		// If init didn't catch it (because Next returned false but Err was set),
		// check if Next fails.
		// Actually NewMergeIterator checks Err() if Next() returns false.
		// So mi.err should be set.
		assert.Error(t, mi.Err())
	}
}

func TestCoalescingIterator_Basic(t *testing.T) {
	// Input: Insert(A), Update(A), Insert(B)
	input := []*events.StoreChangeEvent{
		{EventID: "1", MgoDocID: "A", OpType: events.StoreOperationInsert, ClusterTime: events.ClusterTime{T: 100}},
		{EventID: "2", MgoDocID: "A", OpType: events.StoreOperationUpdate, ClusterTime: events.ClusterTime{T: 200}},
		{EventID: "3", MgoDocID: "B", OpType: events.StoreOperationInsert, ClusterTime: events.ClusterTime{T: 300}},
	}
	iter := NewMockIterator(input)

	// Batch size 10 (all fit in one batch)
	ci := NewCoalescingIterator(iter, 10)
	defer ci.Close()

	// Expect: Insert(A) [merged], Insert(B)
	// Note: Coalescer merges Insert+Update -> Insert with latest state
	// So we expect 2 events.

	assert.True(t, ci.Next())
	assert.Equal(t, "A", ci.Event().MgoDocID)
	assert.Equal(t, events.StoreOperationInsert, ci.Event().OpType)
	// Should have latest timestamp/ID from the update if merged correctly?
	// Coalescer logic: Insert + Update = Insert.
	// The event ID usually takes the latest one.
	assert.Equal(t, "2", ci.Event().EventID)

	assert.True(t, ci.Next())
	assert.Equal(t, "B", ci.Event().MgoDocID)

	assert.False(t, ci.Next())
}

func TestCoalescingIterator_Batching(t *testing.T) {
	// Input: A, A, A (3 updates)
	input := []*events.StoreChangeEvent{
		{EventID: "1", MgoDocID: "A", OpType: events.StoreOperationUpdate, ClusterTime: events.ClusterTime{T: 100}},
		{EventID: "2", MgoDocID: "A", OpType: events.StoreOperationUpdate, ClusterTime: events.ClusterTime{T: 200}},
		{EventID: "3", MgoDocID: "A", OpType: events.StoreOperationUpdate, ClusterTime: events.ClusterTime{T: 300}},
	}
	iter := NewMockIterator(input)

	// Batch size 2.
	// Batch 1: 1, 2 -> Merged to Update(A, T=200)
	// Batch 2: 3    -> Update(A, T=300)
	ci := NewCoalescingIterator(iter, 2)
	defer ci.Close()

	assert.True(t, ci.Next())
	assert.Equal(t, "2", ci.Event().EventID)

	assert.True(t, ci.Next())
	assert.Equal(t, "3", ci.Event().EventID)

	assert.False(t, ci.Next())
}

func TestCoalescingIterator_Cancellation(t *testing.T) {
	// Input: Insert(A), Delete(A) -> Should cancel out
	input := []*events.StoreChangeEvent{
		{EventID: "1", MgoDocID: "A", OpType: events.StoreOperationInsert, ClusterTime: events.ClusterTime{T: 100}},
		{EventID: "2", MgoDocID: "A", OpType: events.StoreOperationDelete, ClusterTime: events.ClusterTime{T: 200}},
		{EventID: "3", MgoDocID: "B", OpType: events.StoreOperationInsert, ClusterTime: events.ClusterTime{T: 300}},
	}
	iter := NewMockIterator(input)

	ci := NewCoalescingIterator(iter, 10)
	defer ci.Close()

	// Should skip A entirely and return B
	assert.True(t, ci.Next())
	assert.Equal(t, "B", ci.Event().MgoDocID)

	assert.False(t, ci.Next())
}

func TestCoalescingIterator_EdgeCases(t *testing.T) {
	// Test Event() when buffer is nil
	iter := NewCoalescingIterator(NewMockIterator(nil), 10)
	assert.Nil(t, iter.Event())

	// Test Err()
	mockIter := NewMockIterator(nil)
	mockIter.err = errors.New("source error")
	iter = NewCoalescingIterator(mockIter, 10)
	assert.False(t, iter.Next())
	assert.Error(t, iter.Err())
}

func TestMergeIterator_Less_TieBreaker(t *testing.T) {
	// Two events with same ClusterTime but different Backend
	evt1 := &events.StoreChangeEvent{
		ClusterTime: events.ClusterTime{T: 100, I: 1},
		Backend:     "backend1",
	}
	evt2 := &events.StoreChangeEvent{
		ClusterTime: events.ClusterTime{T: 100, I: 1},
		Backend:     "backend2",
	}

	iter1 := NewMockIterator([]*events.StoreChangeEvent{evt1})
	iter2 := NewMockIterator([]*events.StoreChangeEvent{evt2})

	mi := NewMergeIterator([]events.Iterator{iter1, iter2})

	// Should return backend1 first
	assert.True(t, mi.Next())
	assert.Equal(t, "backend1", mi.Event().Backend)

	assert.True(t, mi.Next())
	assert.Equal(t, "backend2", mi.Event().Backend)

	assert.False(t, mi.Next())
}

func TestMergeIterator_Error_New(t *testing.T) {
	iter1 := NewMockIterator([]*events.StoreChangeEvent{})
	iter1.err = errors.New("iterator error")

	mi := NewMergeIterator([]events.Iterator{iter1})

	assert.False(t, mi.Next())
	assert.Error(t, mi.Err())
}

func TestMergeIterator_Empty_New(t *testing.T) {
	mi := NewMergeIterator([]events.Iterator{})
	assert.False(t, mi.Next())
}
