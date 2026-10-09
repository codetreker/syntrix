package mem_store

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewDatabase(t *testing.T) {
	db := NewDatabase("myapp")

	assert.Equal(t, "myapp", db.Name)
	assert.Equal(t, 0, db.IndexCount())
}

func TestIndexKey(t *testing.T) {
	tests := []struct {
		pattern    string
		templateID string
		want       string
	}{
		{"users/*/chats", "ts:desc", "users/*/chats|ts:desc"},
		{"rooms/*/messages", "name:asc,age:desc", "rooms/*/messages|name:asc,age:desc"},
		{"simple", "id", "simple|id"},
	}

	for _, tt := range tests {
		got := IndexKey(tt.pattern, tt.templateID)
		assert.Equal(t, tt.want, got)
	}
}

func TestDatabase_GetIndex_NotFound(t *testing.T) {
	db := NewDatabase("myapp")

	index := db.GetIndex("users/*/chats", "ts:desc")
	assert.Nil(t, index)
}

func TestDatabase_GetOrCreateIndex(t *testing.T) {
	db := NewDatabase("myapp")

	// First call creates the index
	s1 := db.GetOrCreateIndex("users/*/chats", "ts:desc", "users/{uid}/chats")
	assert.NotNil(t, s1)
	assert.Equal(t, "users/*/chats", s1.Pattern)
	assert.Equal(t, "ts:desc", s1.TemplateID)
	assert.Equal(t, "users/{uid}/chats", s1.RawPattern)
	assert.Equal(t, 1, db.IndexCount())

	// Second call returns the same index
	s2 := db.GetOrCreateIndex("users/*/chats", "ts:desc", "users/{uid}/chats")
	assert.Same(t, s1, s2)
	assert.Equal(t, 1, db.IndexCount())

	// Different template creates a new index
	s3 := db.GetOrCreateIndex("users/*/chats", "name:asc", "users/{uid}/chats")
	assert.NotSame(t, s1, s3)
	assert.Equal(t, 2, db.IndexCount())
}

func TestDatabase_GetIndex(t *testing.T) {
	db := NewDatabase("myapp")

	// Create index
	created := db.GetOrCreateIndex("users/*/chats", "ts:desc", "users/{uid}/chats")

	// Get index
	found := db.GetIndex("users/*/chats", "ts:desc")
	assert.Same(t, created, found)

	// Get non-existent index
	notFound := db.GetIndex("other/*/path", "id")
	assert.Nil(t, notFound)
}

func TestDatabase_DeleteIndex(t *testing.T) {
	db := NewDatabase("myapp")

	db.GetOrCreateIndex("users/*/chats", "ts:desc", "users/{uid}/chats")
	db.GetOrCreateIndex("rooms/*/messages", "ts:desc", "rooms/{rid}/messages")
	assert.Equal(t, 2, db.IndexCount())

	// Delete first index
	db.DeleteIndex("users/*/chats", "ts:desc")
	assert.Equal(t, 1, db.IndexCount())
	assert.Nil(t, db.GetIndex("users/*/chats", "ts:desc"))
	assert.NotNil(t, db.GetIndex("rooms/*/messages", "ts:desc"))

	// Delete non-existent index (no-op)
	db.DeleteIndex("nonexistent", "id")
	assert.Equal(t, 1, db.IndexCount())
}

func TestDatabase_ListIndexes(t *testing.T) {
	db := NewDatabase("myapp")

	db.GetOrCreateIndex("users/*/chats", "ts:desc", "users/{uid}/chats")
	db.GetOrCreateIndex("rooms/*/messages", "ts:desc", "rooms/{rid}/messages")
	db.GetOrCreateIndex("users/*/chats", "name:asc", "users/{uid}/chats")

	indexes := db.ListIndexes()
	assert.Len(t, indexes, 3)

	// Verify all indexes are present
	patterns := make(map[string]bool)
	for _, s := range indexes {
		patterns[IndexKey(s.Pattern, s.TemplateID)] = true
	}

	assert.True(t, patterns["users/*/chats|ts:desc"])
	assert.True(t, patterns["rooms/*/messages|ts:desc"])
	assert.True(t, patterns["users/*/chats|name:asc"])
}

func TestDatabase_Clear(t *testing.T) {
	db := NewDatabase("myapp")

	db.GetOrCreateIndex("users/*/chats", "ts:desc", "users/{uid}/chats")
	db.GetOrCreateIndex("rooms/*/messages", "ts:desc", "rooms/{rid}/messages")
	assert.Equal(t, 2, db.IndexCount())

	db.Clear()
	assert.Equal(t, 0, db.IndexCount())
	assert.Len(t, db.ListIndexes(), 0)

	// Can still create indexes after clear
	db.GetOrCreateIndex("new/*/path", "id", "new/{x}/path")
	assert.Equal(t, 1, db.IndexCount())
}

func TestDatabase_ConcurrentGetOrCreate(t *testing.T) {
	db := NewDatabase("myapp")

	var wg sync.WaitGroup
	indexes := make([]*Index, 100)

	// Create 100 goroutines all trying to create the same index
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			indexes[idx] = db.GetOrCreateIndex("users/*/chats", "ts:desc", "users/{uid}/chats")
		}(i)
	}

	wg.Wait()

	// All should return the same index
	for i := 1; i < 100; i++ {
		assert.Same(t, indexes[0], indexes[i], "index %d differs from index 0", i)
	}

	// Only one index should exist
	assert.Equal(t, 1, db.IndexCount())
}

func TestDatabase_ConcurrentAccess(t *testing.T) {
	db := NewDatabase("myapp")

	var wg sync.WaitGroup

	// Create indexes concurrently
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pattern := "path" + string(rune('a'+idx%10)) + "/*/items"
			db.GetOrCreateIndex(pattern, "ts:desc", pattern)
		}(i)
	}

	// Delete indexes concurrently
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pattern := "path" + string(rune('a'+idx%10)) + "/*/items"
			db.DeleteIndex(pattern, "ts:desc")
		}(i)
	}

	// List indexes concurrently
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = db.ListIndexes()
		}()
	}

	wg.Wait()
}

func TestDatabase_IndexIsolation(t *testing.T) {
	db := NewDatabase("myapp")

	// Create two indexes for the same pattern but different templates
	s1 := db.GetOrCreateIndex("users/*/chats", "ts:desc", "users/{uid}/chats")
	s2 := db.GetOrCreateIndex("users/*/chats", "name:asc", "users/{uid}/chats")

	// Insert into s1
	s1.Upsert("doc1", []byte{0x10})
	s1.Upsert("doc2", []byte{0x20})

	// Insert into s2
	s2.Upsert("doc3", []byte{0x30})

	// Verify isolation
	assert.Equal(t, 2, s1.Len())
	assert.Equal(t, 1, s2.Len())

	// Verify data
	assert.NotNil(t, s1.Get("doc1"))
	assert.NotNil(t, s1.Get("doc2"))
	assert.Nil(t, s1.Get("doc3"))

	assert.Nil(t, s2.Get("doc1"))
	assert.Nil(t, s2.Get("doc2"))
	assert.NotNil(t, s2.Get("doc3"))
}
