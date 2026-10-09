package mem_store

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	s := NewIndex("users/*/chats", "timestamp:desc", "users/{uid}/chats")

	assert.Equal(t, "users/*/chats", s.Pattern)
	assert.Equal(t, "timestamp:desc", s.TemplateID)
	assert.Equal(t, "users/{uid}/chats", s.RawPattern)
	assert.Equal(t, 0, s.Len())
}

func TestIndex_Upsert(t *testing.T) {
	s := NewIndex("users/*/chats", "ts:desc", "users/{uid}/chats")

	// Insert first document
	s.Upsert("doc1", []byte{0x01, 0x00, 0x10})
	assert.Equal(t, 1, s.Len())
	assert.Equal(t, []byte{0x01, 0x00, 0x10}, s.Get("doc1"))

	// Insert second document
	s.Upsert("doc2", []byte{0x01, 0x00, 0x20})
	assert.Equal(t, 2, s.Len())

	// Update first document with new OrderKey
	s.Upsert("doc1", []byte{0x01, 0x00, 0x05})
	assert.Equal(t, 2, s.Len()) // still 2 docs
	assert.Equal(t, []byte{0x01, 0x00, 0x05}, s.Get("doc1"))
}

func TestIndex_Delete(t *testing.T) {
	s := NewIndex("users/*/chats", "ts:desc", "users/{uid}/chats")

	s.Upsert("doc1", []byte{0x01, 0x00, 0x10})
	s.Upsert("doc2", []byte{0x01, 0x00, 0x20})
	assert.Equal(t, 2, s.Len())

	// Delete doc1
	s.Delete("doc1")
	assert.Equal(t, 1, s.Len())
	assert.Nil(t, s.Get("doc1"))
	assert.NotNil(t, s.Get("doc2"))

	// Delete non-existent doc (no-op)
	s.Delete("doc999")
	assert.Equal(t, 1, s.Len())

	// Delete remaining doc
	s.Delete("doc2")
	assert.Equal(t, 0, s.Len())
}

func TestIndex_Search_Basic(t *testing.T) {
	s := NewIndex("users/*/chats", "ts:desc", "users/{uid}/chats")

	// Insert documents with different OrderKeys
	s.Upsert("doc1", []byte{0x01, 0x00, 0x10})
	s.Upsert("doc2", []byte{0x01, 0x00, 0x20})
	s.Upsert("doc3", []byte{0x01, 0x00, 0x30})

	// Search all
	results := s.Search(SearchOptions{Limit: 100})
	assert.Len(t, results, 3)

	// Results should be in OrderKey order
	assert.Equal(t, "doc1", results[0].ID)
	assert.Equal(t, "doc2", results[1].ID)
	assert.Equal(t, "doc3", results[2].ID)
}

func TestIndex_Search_Limit(t *testing.T) {
	s := NewIndex("test", "id", "test")

	for i := 0; i < 10; i++ {
		s.Upsert(fmt.Sprintf("doc%d", i), []byte{byte(i)})
	}

	results := s.Search(SearchOptions{Limit: 3})
	assert.Len(t, results, 3)
	assert.Equal(t, "doc0", results[0].ID)
	assert.Equal(t, "doc1", results[1].ID)
	assert.Equal(t, "doc2", results[2].ID)
}

func TestIndex_Search_LowerBound(t *testing.T) {
	s := NewIndex("test", "id", "test")

	s.Upsert("a", []byte{0x10})
	s.Upsert("b", []byte{0x20})
	s.Upsert("c", []byte{0x30})
	s.Upsert("d", []byte{0x40})

	// Search with lower bound
	results := s.Search(SearchOptions{
		Lower: []byte{0x25},
		Limit: 100,
	})

	assert.Len(t, results, 2)
	assert.Equal(t, "c", results[0].ID)
	assert.Equal(t, "d", results[1].ID)
}

func TestIndex_Search_UpperBound(t *testing.T) {
	s := NewIndex("test", "id", "test")

	s.Upsert("a", []byte{0x10})
	s.Upsert("b", []byte{0x20})
	s.Upsert("c", []byte{0x30})
	s.Upsert("d", []byte{0x40})

	// Search with upper bound (exclusive)
	results := s.Search(SearchOptions{
		Upper: []byte{0x30},
		Limit: 100,
	})

	assert.Len(t, results, 2)
	assert.Equal(t, "a", results[0].ID)
	assert.Equal(t, "b", results[1].ID)
}

func TestIndex_Search_Range(t *testing.T) {
	s := NewIndex("test", "id", "test")

	s.Upsert("a", []byte{0x10})
	s.Upsert("b", []byte{0x20})
	s.Upsert("c", []byte{0x30})
	s.Upsert("d", []byte{0x40})

	// Search with range
	results := s.Search(SearchOptions{
		Lower: []byte{0x15},
		Upper: []byte{0x35},
		Limit: 100,
	})

	assert.Len(t, results, 2)
	assert.Equal(t, "b", results[0].ID)
	assert.Equal(t, "c", results[1].ID)
}

func TestIndex_Search_StartAfter(t *testing.T) {
	s := NewIndex("test", "id", "test")

	s.Upsert("a", []byte{0x10})
	s.Upsert("b", []byte{0x20})
	s.Upsert("c", []byte{0x30})
	s.Upsert("d", []byte{0x40})

	// Search starting after "b" (exclusive)
	results := s.Search(SearchOptions{
		StartAfter: []byte{0x20},
		Limit:      100,
	})

	assert.Len(t, results, 2)
	assert.Equal(t, "c", results[0].ID)
	assert.Equal(t, "d", results[1].ID)
}

func TestIndex_Search_StartAfter_NotFound(t *testing.T) {
	s := NewIndex("test", "id", "test")

	s.Upsert("a", []byte{0x10})
	s.Upsert("b", []byte{0x20})
	s.Upsert("c", []byte{0x30})

	// StartAfter a non-existent key
	results := s.Search(SearchOptions{
		StartAfter: []byte{0x15},
		Limit:      100,
	})

	assert.Len(t, results, 2)
	assert.Equal(t, "b", results[0].ID)
	assert.Equal(t, "c", results[1].ID)
}

func TestIndex_Search_DefaultLimit(t *testing.T) {
	s := NewIndex("test", "id", "test")

	for i := 0; i < 2000; i++ {
		key := make([]byte, 2)
		key[0] = byte(i >> 8)
		key[1] = byte(i & 0xFF)
		s.Upsert(fmt.Sprintf("doc%d", i), key)
	}

	// Search with no limit specified
	results := s.Search(SearchOptions{})
	assert.Len(t, results, 1000) // default limit
}

func TestIndex_Clear(t *testing.T) {
	s := NewIndex("test", "id", "test")

	s.Upsert("a", []byte{0x10})
	s.Upsert("b", []byte{0x20})
	assert.Equal(t, 2, s.Len())

	s.Clear()
	assert.Equal(t, 0, s.Len())
	assert.Nil(t, s.Get("a"))
	assert.Nil(t, s.Get("b"))

	// Can still insert after clear
	s.Upsert("c", []byte{0x30})
	assert.Equal(t, 1, s.Len())
}

func TestIndex_ConcurrentAccess(t *testing.T) {
	s := NewIndex("test", "id", "test")
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 1000; i++ {
			s.Upsert(fmt.Sprintf("doc%d", i), []byte{byte(i % 256)})
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 1000; i++ {
			_ = s.Search(SearchOptions{Limit: 10})
		}
		done <- true
	}()

	// Deleter goroutine
	go func() {
		for i := 0; i < 1000; i++ {
			s.Delete(fmt.Sprintf("doc%d", i%100))
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done
	<-done
}

func TestIndex_OrderPreservation(t *testing.T) {
	s := NewIndex("test", "id", "test")

	// Insert documents in random order
	keys := [][]byte{
		{0x05},
		{0x02},
		{0x08},
		{0x01},
		{0x06},
	}

	for i, key := range keys {
		s.Upsert(fmt.Sprintf("doc%d", i), key)
	}

	// Search should return in sorted order
	results := s.Search(SearchOptions{Limit: 100})
	require.Len(t, results, 5)

	// Verify sorted order
	for i := 1; i < len(results); i++ {
		assert.True(t, string(results[i-1].OrderKey) < string(results[i].OrderKey),
			"results not in order at position %d", i)
	}
}

func TestIndex_UpdateChangesOrder(t *testing.T) {
	s := NewIndex("test", "id", "test")

	// Insert documents
	s.Upsert("doc1", []byte{0x10})
	s.Upsert("doc2", []byte{0x20})
	s.Upsert("doc3", []byte{0x30})

	results := s.Search(SearchOptions{Limit: 100})
	assert.Equal(t, "doc1", results[0].ID)
	assert.Equal(t, "doc2", results[1].ID)
	assert.Equal(t, "doc3", results[2].ID)

	// Update doc1 to have the largest key
	s.Upsert("doc1", []byte{0x40})

	results = s.Search(SearchOptions{Limit: 100})
	assert.Equal(t, "doc2", results[0].ID)
	assert.Equal(t, "doc3", results[1].ID)
	assert.Equal(t, "doc1", results[2].ID)
}

func TestIndex_Search_EmptyIndex(t *testing.T) {
	s := NewIndex("test", "id", "test")

	results := s.Search(SearchOptions{Limit: 100})
	assert.Len(t, results, 0)
}

func TestState_String(t *testing.T) {
	tests := []struct {
		state State
		want  string
	}{
		{StateHealthy, "healthy"},
		{StateRebuilding, "rebuilding"},
		{StateFailed, "failed"},
		{State(99), "unknown"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.state.String())
	}
}

func TestIndex_SameOrderKeyDifferentDocs(t *testing.T) {
	// When two documents have the same OrderKey, they should still be stored separately
	// Note: In practice, OrderKey should include docID as tie-breaker to prevent this.
	// But the index itself should handle this gracefully by using (orderKey, id) as the key.
	s := NewIndex("test", "id", "test")

	// Insert two docs with same OrderKey but different IDs
	// The second insert will update doc1's position in the tree but they're different docs
	s.Upsert("doc1", []byte{0x10})
	s.Upsert("doc2", []byte{0x10})

	assert.Equal(t, 2, s.Len())

	// Both should be retrievable by ID
	assert.Equal(t, []byte{0x10}, s.Get("doc1"))
	assert.Equal(t, []byte{0x10}, s.Get("doc2"))

	// Search should return both (order may vary for same key)
	results := s.Search(SearchOptions{Limit: 100})
	assert.Len(t, results, 2)
}

func TestIndex_State(t *testing.T) {
	s := NewIndex("test", "id", "test")

	// Default state is healthy (zero value)
	assert.Equal(t, StateHealthy, s.State())
	assert.True(t, s.IsHealthy())

	// Set to rebuilding
	s.SetState(StateRebuilding)
	assert.Equal(t, StateRebuilding, s.State())
	assert.False(t, s.IsHealthy())

	// Set to failed
	s.SetState(StateFailed)
	assert.Equal(t, StateFailed, s.State())
	assert.False(t, s.IsHealthy())

	// Set back to healthy
	s.SetState(StateHealthy)
	assert.Equal(t, StateHealthy, s.State())
	assert.True(t, s.IsHealthy())
}
