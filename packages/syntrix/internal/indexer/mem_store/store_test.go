package mem_store

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/codetreker/syntrix/internal/indexer/store"
)

func TestStore_New(t *testing.T) {
	s := New()
	require.NotNil(t, s)
	assert.Empty(t, s.databases)
	assert.Empty(t, s.progress)
}

func TestStore_getOrCreateDB(t *testing.T) {
	s := New()

	// First call creates database
	db1 := s.getOrCreateDB("testdb")
	require.NotNil(t, db1)
	assert.Len(t, s.databases, 1)

	// Second call returns existing database
	db2 := s.getOrCreateDB("testdb")
	assert.Same(t, db1, db2)
	assert.Len(t, s.databases, 1)

	// Different name creates new database
	db3 := s.getOrCreateDB("otherdb")
	require.NotNil(t, db3)
	assert.NotSame(t, db1, db3)
	assert.Len(t, s.databases, 2)
}

func TestStore_Upsert(t *testing.T) {
	s := New()

	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01, 0x02}, "")
	require.NoError(t, err)

	// Verify database and index were created
	assert.Len(t, s.databases, 1)
	db := s.databases["testdb"]
	require.NotNil(t, db)

	idx := db.GetIndex("users/*", "tmpl1")
	require.NotNil(t, idx)

	// Verify document was inserted
	orderKey := idx.Get("doc1")
	assert.Equal(t, []byte{0x01, 0x02}, orderKey)
}

func TestStore_UpsertWithProgress(t *testing.T) {
	s := New()

	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "event-123")
	require.NoError(t, err)

	// Verify progress was saved
	assert.Equal(t, "event-123", s.progress)

	// Update with new progress
	err = s.Upsert("testdb", "users/*", "tmpl1", "doc2", []byte{0x02}, "event-456")
	require.NoError(t, err)
	assert.Equal(t, "event-456", s.progress)

	// Empty progress should not update
	err = s.Upsert("testdb", "users/*", "tmpl1", "doc3", []byte{0x03}, "")
	require.NoError(t, err)
	assert.Equal(t, "event-456", s.progress)
}

func TestStore_UpsertUpdate(t *testing.T) {
	s := New()

	// Initial insert
	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Update with new orderKey
	err = s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x02}, "")
	require.NoError(t, err)

	// Verify update
	db := s.databases["testdb"]
	idx := db.GetIndex("users/*", "tmpl1")
	orderKey := idx.Get("doc1")
	assert.Equal(t, []byte{0x02}, orderKey)
}

func TestStore_Delete(t *testing.T) {
	s := New()

	// Insert a document
	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Delete the document
	err = s.Delete("testdb", "users/*", "tmpl1", "doc1", "")
	require.NoError(t, err)

	// Verify deletion
	db := s.databases["testdb"]
	idx := db.GetIndex("users/*", "tmpl1")
	orderKey := idx.Get("doc1")
	assert.Nil(t, orderKey)
}

func TestStore_DeleteWithProgress(t *testing.T) {
	s := New()

	// Insert a document
	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Delete with progress
	err = s.Delete("testdb", "users/*", "tmpl1", "doc1", "event-789")
	require.NoError(t, err)
	assert.Equal(t, "event-789", s.progress)
}

func TestStore_DeleteNonexistentDB(t *testing.T) {
	s := New()

	// Delete from non-existent database should succeed
	err := s.Delete("nonexistent", "users/*", "tmpl1", "doc1", "")
	require.NoError(t, err)

	// Should update progress even if database doesn't exist
	err = s.Delete("nonexistent", "users/*", "tmpl1", "doc1", "event-abc")
	require.NoError(t, err)
	assert.Equal(t, "event-abc", s.progress)
}

func TestStore_DeleteNonexistentIndex(t *testing.T) {
	s := New()

	// Create database with one index
	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Delete from non-existent index should succeed
	err = s.Delete("testdb", "posts/*", "tmpl2", "doc1", "")
	require.NoError(t, err)
}

func TestStore_Get(t *testing.T) {
	s := New()

	// Insert a document
	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01, 0x02, 0x03}, "")
	require.NoError(t, err)

	// Get should return the orderKey
	orderKey, found := s.Get("testdb", "users/*", "tmpl1", "doc1")
	require.True(t, found)
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, orderKey)
}

func TestStore_GetNonexistentDB(t *testing.T) {
	s := New()

	orderKey, found := s.Get("nonexistent", "users/*", "tmpl1", "doc1")
	assert.False(t, found)
	assert.Nil(t, orderKey)
}

func TestStore_GetNonexistentIndex(t *testing.T) {
	s := New()

	// Create database with one index
	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Get from non-existent index
	orderKey, found := s.Get("testdb", "posts/*", "tmpl2", "doc1")
	assert.False(t, found)
	assert.Nil(t, orderKey)
}

func TestStore_GetNonexistentDoc(t *testing.T) {
	s := New()

	// Create database with one document
	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Get non-existent document
	orderKey, found := s.Get("testdb", "users/*", "tmpl1", "doc2")
	assert.False(t, found)
	assert.Nil(t, orderKey)
}

func TestStore_GetReturnsCopy(t *testing.T) {
	s := New()

	original := []byte{0x01, 0x02, 0x03}
	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", original, "")
	require.NoError(t, err)

	// Get should return a copy
	retrieved, found := s.Get("testdb", "users/*", "tmpl1", "doc1")
	require.True(t, found)

	// Modify the retrieved slice
	retrieved[0] = 0xFF

	// Original should be unchanged
	retrieved2, found := s.Get("testdb", "users/*", "tmpl1", "doc1")
	require.True(t, found)
	assert.Equal(t, byte(0x01), retrieved2[0])
}

func TestStore_Search(t *testing.T) {
	s := New()

	// Insert multiple documents
	docs := []struct {
		id       string
		orderKey []byte
	}{
		{"doc1", []byte{0x00, 0x01}},
		{"doc2", []byte{0x00, 0x02}},
		{"doc3", []byte{0x00, 0x03}},
		{"doc4", []byte{0x00, 0x04}},
		{"doc5", []byte{0x00, 0x05}},
	}

	for _, doc := range docs {
		err := s.Upsert("testdb", "users/*", "tmpl1", doc.id, doc.orderKey, "")
		require.NoError(t, err)
	}

	// Search all
	results, err := s.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, results, 5)

	// Results should be ordered
	for i := 0; i < len(results)-1; i++ {
		assert.True(t, bytes.Compare(results[i].OrderKey, results[i+1].OrderKey) < 0)
	}
}

func TestStore_SearchWithBounds(t *testing.T) {
	s := New()

	// Insert documents
	docs := []struct {
		id       string
		orderKey []byte
	}{
		{"doc1", []byte{0x00, 0x01}},
		{"doc2", []byte{0x00, 0x02}},
		{"doc3", []byte{0x00, 0x03}},
		{"doc4", []byte{0x00, 0x04}},
		{"doc5", []byte{0x00, 0x05}},
	}

	for _, doc := range docs {
		err := s.Upsert("testdb", "users/*", "tmpl1", doc.id, doc.orderKey, "")
		require.NoError(t, err)
	}

	// Search with lower bound (inclusive)
	results, err := s.Search("testdb", "users/*", "tmpl1", store.SearchOptions{
		Lower: []byte{0x00, 0x02},
		Limit: 10,
	})
	require.NoError(t, err)
	assert.Len(t, results, 4) // doc2, doc3, doc4, doc5

	// Search with upper bound (exclusive)
	results, err = s.Search("testdb", "users/*", "tmpl1", store.SearchOptions{
		Upper: []byte{0x00, 0x04},
		Limit: 10,
	})
	require.NoError(t, err)
	assert.Len(t, results, 3) // doc1, doc2, doc3

	// Search with both bounds
	results, err = s.Search("testdb", "users/*", "tmpl1", store.SearchOptions{
		Lower: []byte{0x00, 0x02},
		Upper: []byte{0x00, 0x04},
		Limit: 10,
	})
	require.NoError(t, err)
	assert.Len(t, results, 2) // doc2, doc3
}

func TestStore_SearchWithLimit(t *testing.T) {
	s := New()

	// Insert 10 documents
	for i := 0; i < 10; i++ {
		docID := string(rune('a' + i))
		orderKey := []byte{byte(i)}
		err := s.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, "")
		require.NoError(t, err)
	}

	// Search with limit
	results, err := s.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 5})
	require.NoError(t, err)
	assert.Len(t, results, 5)
}

func TestStore_SearchWithStartAfter(t *testing.T) {
	s := New()

	// Insert documents
	docs := []struct {
		id       string
		orderKey []byte
	}{
		{"doc1", []byte{0x00, 0x01}},
		{"doc2", []byte{0x00, 0x02}},
		{"doc3", []byte{0x00, 0x03}},
		{"doc4", []byte{0x00, 0x04}},
		{"doc5", []byte{0x00, 0x05}},
	}

	for _, doc := range docs {
		err := s.Upsert("testdb", "users/*", "tmpl1", doc.id, doc.orderKey, "")
		require.NoError(t, err)
	}

	// Search with StartAfter (cursor-based pagination)
	results, err := s.Search("testdb", "users/*", "tmpl1", store.SearchOptions{
		StartAfter: []byte{0x00, 0x02},
		Limit:      10,
	})
	require.NoError(t, err)
	assert.Len(t, results, 3) // doc3, doc4, doc5
	assert.Equal(t, "doc3", results[0].ID)
}

func TestStore_SearchNonexistentDB(t *testing.T) {
	s := New()

	results, err := s.Search("nonexistent", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestStore_SearchNonexistentIndex(t *testing.T) {
	s := New()

	// Create database with one index
	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Search in non-existent index
	results, err := s.Search("testdb", "posts/*", "tmpl2", store.SearchOptions{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestStore_DeleteIndex(t *testing.T) {
	s := New()

	// Create index with multiple documents
	for i := 0; i < 5; i++ {
		err := s.Upsert("testdb", "users/*", "tmpl1", string(rune('a'+i)), []byte{byte(i)}, "")
		require.NoError(t, err)
	}

	// Delete index
	err := s.DeleteIndex("testdb", "users/*", "tmpl1")
	require.NoError(t, err)

	// Search should return empty
	results, err := s.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestStore_DeleteIndexNonexistentDB(t *testing.T) {
	s := New()

	// Should not error
	err := s.DeleteIndex("nonexistent", "users/*", "tmpl1")
	require.NoError(t, err)
}

func TestStore_SetState(t *testing.T) {
	s := New()

	// SetState creates the index if it doesn't exist
	err := s.SetState("testdb", "users/*", "tmpl1", store.IndexStateRebuilding)
	require.NoError(t, err)

	// Verify state
	state, err := s.GetState("testdb", "users/*", "tmpl1")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateRebuilding, state)

	// Update state
	err = s.SetState("testdb", "users/*", "tmpl1", store.IndexStateFailed)
	require.NoError(t, err)

	state, err = s.GetState("testdb", "users/*", "tmpl1")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateFailed, state)

	// Set to healthy
	err = s.SetState("testdb", "users/*", "tmpl1", store.IndexStateHealthy)
	require.NoError(t, err)

	state, err = s.GetState("testdb", "users/*", "tmpl1")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateHealthy, state)
}

func TestStore_SetStateUnknown(t *testing.T) {
	s := New()

	// Unknown state should default to healthy
	err := s.SetState("testdb", "users/*", "tmpl1", store.IndexState("unknown"))
	require.NoError(t, err)

	state, err := s.GetState("testdb", "users/*", "tmpl1")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateHealthy, state)
}

func TestStore_GetState(t *testing.T) {
	s := New()

	// Non-existent database returns healthy
	state, err := s.GetState("nonexistent", "users/*", "tmpl1")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateHealthy, state)

	// Create database but not the specific index
	err = s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	// Non-existent index returns healthy
	state, err = s.GetState("testdb", "posts/*", "tmpl2")
	require.NoError(t, err)
	assert.Equal(t, store.IndexStateHealthy, state)
}

func TestStore_GetStateAllStates(t *testing.T) {
	s := New()

	// Test all state transitions
	states := []store.IndexState{
		store.IndexStateHealthy,
		store.IndexStateRebuilding,
		store.IndexStateFailed,
	}

	for _, expected := range states {
		err := s.SetState("testdb", "users/*", "tmpl1", expected)
		require.NoError(t, err)

		state, err := s.GetState("testdb", "users/*", "tmpl1")
		require.NoError(t, err)
		assert.Equal(t, expected, state)
	}
}

func TestStore_LoadProgress(t *testing.T) {
	s := New()

	// Initially empty
	progress, err := s.LoadProgress()
	require.NoError(t, err)
	assert.Empty(t, progress)

	// After saving progress via Upsert
	err = s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "event-123")
	require.NoError(t, err)

	progress, err = s.LoadProgress()
	require.NoError(t, err)
	assert.Equal(t, "event-123", progress)
}

func TestStore_Flush(t *testing.T) {
	s := New()

	// Flush is a no-op for memory store
	err := s.Flush()
	require.NoError(t, err)
}

func TestStore_Close(t *testing.T) {
	s := New()

	// Close is a no-op for memory store
	err := s.Close()
	require.NoError(t, err)

	// Can close multiple times
	err = s.Close()
	require.NoError(t, err)
}

func TestStore_ListDatabases(t *testing.T) {
	s := New()

	// Initially empty
	dbs, _ := s.ListDatabases()
	assert.Empty(t, dbs)

	// Create databases
	err := s.Upsert("db1", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)
	err = s.Upsert("db2", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)
	err = s.Upsert("db3", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	dbs, _ = s.ListDatabases()
	assert.Len(t, dbs, 3)
	assert.Contains(t, dbs, "db1")
	assert.Contains(t, dbs, "db2")
	assert.Contains(t, dbs, "db3")
}

func TestStore_ListIndexes(t *testing.T) {
	s := New()

	// Non-existent database returns nil
	indexes, _ := s.ListIndexes("nonexistent")
	assert.Nil(t, indexes)

	// Create database with multiple indexes
	err := s.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)
	err = s.Upsert("testdb", "posts/*", "tmpl2", "doc1", []byte{0x01}, "")
	require.NoError(t, err)

	indexes, _ = s.ListIndexes("testdb")
	assert.Len(t, indexes, 2)

	// Verify index info
	patterns := make(map[string]bool)
	for _, idx := range indexes {
		patterns[idx.Pattern] = true
	}
	assert.True(t, patterns["users/*"])
	assert.True(t, patterns["posts/*"])
}

func TestStore_InterfaceCompliance(t *testing.T) {
	// Compile-time check
	var _ store.Store = (*Store)(nil)
}

func TestStore_ConcurrentOperations(t *testing.T) {
	s := New()

	const numGoroutines = 100
	done := make(chan struct{}, numGoroutines)

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			docID := string(rune('a'+idx/26)) + string(rune('a'+idx%26))
			s.Upsert("testdb", "users/*", "tmpl1", docID, []byte{byte(idx)}, "")
		}(i)
	}

	// Wait for all writes
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify
	results, err := s.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 200})
	require.NoError(t, err)
	assert.Len(t, results, numGoroutines)
}

func TestStore_ConcurrentReadsAndWrites(t *testing.T) {
	s := New()

	// Insert initial data
	for i := 0; i < 10; i++ {
		s.Upsert("testdb", "users/*", "tmpl1", string(rune('a'+i)), []byte{byte(i)}, "")
	}

	const numGoroutines = 50
	done := make(chan struct{}, numGoroutines*2)

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			s.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
		}()
	}

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			docID := "new" + string(rune('a'+idx))
			s.Upsert("testdb", "users/*", "tmpl1", docID, []byte{byte(100 + idx)}, "")
		}(i)
	}

	// Wait for all operations
	for i := 0; i < numGoroutines*2; i++ {
		<-done
	}
}

func TestStore_DeleteDatabase(t *testing.T) {
	s := New()

	// Create some data in multiple databases
	err := s.Upsert("db1", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	require.NoError(t, err)
	err = s.Upsert("db2", "posts/*", "tmpl2", "doc2", []byte{0x02}, "")
	require.NoError(t, err)

	// Verify both databases exist
	assert.Len(t, s.databases, 2)

	// Delete one database
	err = s.DeleteDatabase("db1")
	require.NoError(t, err)

	// Verify only db2 remains
	assert.Len(t, s.databases, 1)
	assert.Nil(t, s.databases["db1"])
	assert.NotNil(t, s.databases["db2"])

	// Delete non-existent database (should not error)
	err = s.DeleteDatabase("nonexistent")
	require.NoError(t, err)
}

func TestStore_DeleteDatabase_Empty(t *testing.T) {
	s := New()

	// Delete from empty store should not error
	err := s.DeleteDatabase("anydb")
	require.NoError(t, err)
}
