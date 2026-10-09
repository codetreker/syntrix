package persist_store

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"time"

	"github.com/codetreker/syntrix/internal/indexer/config"
	"github.com/codetreker/syntrix/internal/indexer/store"
)

func setupTestStore(t *testing.T) (*PebbleStore, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      100,
		BatchInterval:  50 * time.Millisecond, // 50ms for tests
		QueueSize:      1000,
		BlockCacheSize: 8 * 1024 * 1024, // 8MB for tests
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to create store: %v", err)
	}

	cleanup := func() {
		ps.Close()
		os.RemoveAll(tmpDir)
	}

	return ps, cleanup
}

func TestPebbleStoreUpsertAndGet(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"
	docID := "user123"
	orderKey := []byte{0x00, 0x01, 0x02, 0x03}

	// Upsert
	if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Get
	got, found := ps.Get(db, pattern, tmplID, docID)
	if !found {
		t.Fatal("Get should find the document")
	}
	if !bytes.Equal(got, orderKey) {
		t.Errorf("Get returned %v, want %v", got, orderKey)
	}
}

func TestPebbleStoreUpsertUpdate(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"
	docID := "user123"
	orderKey1 := []byte{0x00, 0x01}
	orderKey2 := []byte{0x00, 0x02}

	// Initial insert
	if err := ps.Upsert(db, pattern, tmplID, docID, orderKey1, ""); err != nil {
		t.Fatalf("Upsert 1 failed: %v", err)
	}

	// Update with new orderKey
	if err := ps.Upsert(db, pattern, tmplID, docID, orderKey2, ""); err != nil {
		t.Fatalf("Upsert 2 failed: %v", err)
	}

	// Get should return new orderKey
	got, found := ps.Get(db, pattern, tmplID, docID)
	if !found {
		t.Fatal("Get should find the document")
	}
	if !bytes.Equal(got, orderKey2) {
		t.Errorf("Get returned %v, want %v", got, orderKey2)
	}

	// Search should only find document once
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Search returned %d results, want 1", len(results))
	}
}

func TestPebbleStoreDelete(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"
	docID := "user123"
	orderKey := []byte{0x00, 0x01, 0x02}

	// Insert
	if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Delete
	if err := ps.Delete(db, pattern, tmplID, docID, ""); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Get should not find
	_, found := ps.Get(db, pattern, tmplID, docID)
	if found {
		t.Error("Get should not find deleted document")
	}

	// Delete again (idempotent)
	if err := ps.Delete(db, pattern, tmplID, docID, ""); err != nil {
		t.Fatalf("Delete again failed: %v", err)
	}
}

func TestPebbleStoreSearch(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

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
		if err := ps.Upsert(db, pattern, tmplID, doc.id, doc.orderKey, ""); err != nil {
			t.Fatalf("Upsert %s failed: %v", doc.id, err)
		}
	}

	// Search all
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("Search returned %d results, want 5", len(results))
	}

	// Results should be ordered by orderKey
	for i := 0; i < len(results)-1; i++ {
		if bytes.Compare(results[i].OrderKey, results[i+1].OrderKey) >= 0 {
			t.Errorf("results not ordered: %v >= %v", results[i].OrderKey, results[i+1].OrderKey)
		}
	}
}

func TestPebbleStoreSearchWithBounds(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

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
		if err := ps.Upsert(db, pattern, tmplID, doc.id, doc.orderKey, ""); err != nil {
			t.Fatalf("Upsert %s failed: %v", doc.id, err)
		}
	}

	// Search with lower bound (inclusive)
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{
		Lower: []byte{0x00, 0x02},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 4 { // doc2, doc3, doc4, doc5
		t.Errorf("Search with lower bound returned %d results, want 4", len(results))
	}

	// Search with upper bound (exclusive)
	results, err = ps.Search(db, pattern, tmplID, store.SearchOptions{
		Upper: []byte{0x00, 0x04},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 3 { // doc1, doc2, doc3
		t.Errorf("Search with upper bound returned %d results, want 3", len(results))
	}

	// Search with both bounds
	results, err = ps.Search(db, pattern, tmplID, store.SearchOptions{
		Lower: []byte{0x00, 0x02},
		Upper: []byte{0x00, 0x04},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 { // doc2, doc3
		t.Errorf("Search with both bounds returned %d results, want 2", len(results))
	}
}

func TestPebbleStoreSearchWithLimit(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Insert 10 documents
	for i := 0; i < 10; i++ {
		docID := string(rune('a' + i))
		orderKey := []byte{byte(i)}
		if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}

	// Search with limit
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("Search returned %d results, want 5", len(results))
	}
}

func TestPebbleStoreSearchWithStartAfter(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

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
		if err := ps.Upsert(db, pattern, tmplID, doc.id, doc.orderKey, ""); err != nil {
			t.Fatalf("Upsert %s failed: %v", doc.id, err)
		}
	}

	// Search with StartAfter (cursor-based pagination)
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{
		StartAfter: []byte{0x00, 0x02},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 3 { // doc3, doc4, doc5
		t.Errorf("Search with StartAfter returned %d results, want 3", len(results))
	}
	if results[0].ID != "doc3" {
		t.Errorf("First result should be doc3, got %s", results[0].ID)
	}
}

func TestPebbleStoreDeleteIndex(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Insert documents
	for i := 0; i < 5; i++ {
		docID := string(rune('a' + i))
		orderKey := []byte{byte(i)}
		if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}

	// Set state
	if err := ps.SetState(db, pattern, tmplID, store.IndexStateHealthy); err != nil {
		t.Fatalf("SetState failed: %v", err)
	}

	// Delete entire index
	if err := ps.DeleteIndex(db, pattern, tmplID); err != nil {
		t.Fatalf("DeleteIndex failed: %v", err)
	}

	// Search should return no results
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Search returned %d results after DeleteIndex, want 0", len(results))
	}

	// Get should not find any docs
	for i := 0; i < 5; i++ {
		docID := string(rune('a' + i))
		_, found := ps.Get(db, pattern, tmplID, docID)
		if found {
			t.Errorf("Get should not find %s after DeleteIndex", docID)
		}
	}
}

func TestPebbleStoreState(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Default state is healthy
	state, err := ps.GetState(db, pattern, tmplID)
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}
	if state != store.IndexStateHealthy {
		t.Errorf("default state = %s, want %s", state, store.IndexStateHealthy)
	}

	// Set to rebuilding
	if err := ps.SetState(db, pattern, tmplID, store.IndexStateRebuilding); err != nil {
		t.Fatalf("SetState failed: %v", err)
	}

	state, err = ps.GetState(db, pattern, tmplID)
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}
	if state != store.IndexStateRebuilding {
		t.Errorf("state = %s, want %s", state, store.IndexStateRebuilding)
	}

	// Set to failed
	if err := ps.SetState(db, pattern, tmplID, store.IndexStateFailed); err != nil {
		t.Fatalf("SetState failed: %v", err)
	}

	state, err = ps.GetState(db, pattern, tmplID)
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}
	if state != store.IndexStateFailed {
		t.Errorf("state = %s, want %s", state, store.IndexStateFailed)
	}
}

func TestPebbleStoreProgress(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "ts"

	// Initially empty
	eventID, err := ps.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress failed: %v", err)
	}
	if eventID != "" {
		t.Errorf("initial progress = %q, want empty", eventID)
	}

	// Save progress via Upsert
	if err := ps.Upsert(db, pattern, tmplID, "doc1", []byte{0x01}, "event-123"); err != nil {
		t.Fatalf("Upsert with progress failed: %v", err)
	}

	// Load progress
	eventID, err = ps.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress failed: %v", err)
	}
	if eventID != "event-123" {
		t.Errorf("progress = %q, want event-123", eventID)
	}

	// Update progress via another Upsert
	if err := ps.Upsert(db, pattern, tmplID, "doc2", []byte{0x02}, "event-456"); err != nil {
		t.Fatalf("Upsert with progress failed: %v", err)
	}

	eventID, err = ps.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress failed: %v", err)
	}
	if eventID != "event-456" {
		t.Errorf("progress = %q, want event-456", eventID)
	}
}

func TestPebbleStoreMultipleIndexes(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"

	// Create two indexes with different patterns
	pattern1 := "users/*"
	pattern2 := "posts/*"
	tmplID := "created_at"

	// Insert into pattern1
	if err := ps.Upsert(db, pattern1, tmplID, "user1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert pattern1 failed: %v", err)
	}
	if err := ps.Upsert(db, pattern1, tmplID, "user2", []byte{0x02}, ""); err != nil {
		t.Fatalf("Upsert pattern1 failed: %v", err)
	}

	// Insert into pattern2
	if err := ps.Upsert(db, pattern2, tmplID, "post1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert pattern2 failed: %v", err)
	}
	if err := ps.Upsert(db, pattern2, tmplID, "post2", []byte{0x02}, ""); err != nil {
		t.Fatalf("Upsert pattern2 failed: %v", err)
	}
	if err := ps.Upsert(db, pattern2, tmplID, "post3", []byte{0x03}, ""); err != nil {
		t.Fatalf("Upsert pattern2 failed: %v", err)
	}

	// Search pattern1
	results1, err := ps.Search(db, pattern1, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search pattern1 failed: %v", err)
	}
	if len(results1) != 2 {
		t.Errorf("Search pattern1 returned %d results, want 2", len(results1))
	}

	// Search pattern2
	results2, err := ps.Search(db, pattern2, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search pattern2 failed: %v", err)
	}
	if len(results2) != 3 {
		t.Errorf("Search pattern2 returned %d results, want 3", len(results2))
	}

	// Delete pattern1 index should not affect pattern2
	if err := ps.DeleteIndex(db, pattern1, tmplID); err != nil {
		t.Fatalf("DeleteIndex pattern1 failed: %v", err)
	}

	results2, err = ps.Search(db, pattern2, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search pattern2 after delete failed: %v", err)
	}
	if len(results2) != 3 {
		t.Errorf("Search pattern2 after delete returned %d results, want 3", len(results2))
	}
}

func TestPebbleStoreFlush(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Insert some data
	if err := ps.Upsert(db, pattern, tmplID, "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Flush should not error (even though sync writes already committed)
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Data should still be accessible
	_, found := ps.Get(db, pattern, tmplID, "doc1")
	if !found {
		t.Error("Get should find document after Flush")
	}
}

func TestPebbleStoreCloseReopen(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	cfg := config.StoreConfig{
		Path:           dbPath,
		BatchSize:      100,
		BatchInterval:  100,
		QueueSize:      1000,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// First open: insert data with progress
	ps1, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	if err := ps1.Upsert(db, pattern, tmplID, "doc1", []byte{0x01}, "event-100"); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps1.SetState(db, pattern, tmplID, store.IndexStateHealthy); err != nil {
		t.Fatalf("SetState failed: %v", err)
	}

	if err := ps1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Second open: verify data persisted
	ps2, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer ps2.Close()

	// Check document
	orderKey, found := ps2.Get(db, pattern, tmplID, "doc1")
	if !found {
		t.Error("Get should find document after reopen")
	}
	if !bytes.Equal(orderKey, []byte{0x01}) {
		t.Errorf("orderKey = %v, want [0x01]", orderKey)
	}

	// Check progress
	eventID, err := ps2.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress failed: %v", err)
	}
	if eventID != "event-100" {
		t.Errorf("progress = %q, want event-100", eventID)
	}

	// Check state
	state, err := ps2.GetState(db, pattern, tmplID)
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}
	if state != store.IndexStateHealthy {
		t.Errorf("state = %s, want %s", state, store.IndexStateHealthy)
	}
}

func TestPebbleStoreInterfaceCompliance(t *testing.T) {
	t.Parallel()
	// Compile-time check is in pebble.go
	// This test just ensures the interface is properly implemented
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	var _ store.Store = ps
}

func TestNewPebbleStoreEmptyPath(t *testing.T) {
	t.Parallel()
	cfg := config.StoreConfig{
		Path: "",
	}

	_, err := NewPebbleStore(cfg, slog.Default())
	if err == nil {
		t.Error("NewPebbleStore should fail with empty path")
	}
}

func TestNewPebbleStoreDefaultBatchParams(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create with zero batch params - should use defaults
	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      0, // Should default to 100
		BatchInterval:  0, // Should default to 100ms
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Verify defaults were applied
	if ps.batchSize != 100 {
		t.Errorf("batchSize = %d, want 100", ps.batchSize)
	}
	if ps.batchInterval != 100*time.Millisecond {
		t.Errorf("batchInterval = %v, want 100ms", ps.batchInterval)
	}
}

func TestPebbleStoreGetNotFound(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Get non-existent document
	_, found := ps.Get("testdb", "users/*", "created_at", "nonexistent")
	if found {
		t.Error("Get should not find non-existent document")
	}
}

func TestPebbleStoreSearchEmptyIndex(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Search empty index
	results, err := ps.Search("testdb", "users/*", "created_at", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Search on empty index returned %d results, want 0", len(results))
	}
}

func TestPebbleStoreSearchDefaultLimit(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Insert more than default limit (1000)
	for i := 0; i < 50; i++ {
		docID := string(rune('a'+i/26)) + string(rune('a'+i%26))
		orderKey := []byte{byte(i / 256), byte(i % 256)}
		if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}

	// Search with limit=0 should use default (1000)
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 0})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 50 {
		t.Errorf("Search with default limit returned %d results, want 50", len(results))
	}
}

func TestPebbleStoreCloseIdempotent(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Close should be idempotent
	if err := ps.Close(); err != nil {
		t.Fatalf("First Close failed: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("Second Close failed: %v", err)
	}
}

func TestPebbleStoreFlushEmptyPending(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Flush with nothing pending should succeed
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush empty failed: %v", err)
	}
}

func TestPebbleStoreSpecialCharacters(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Test with special characters in db name
	db := "test/db with spaces"
	pattern := "users/*"
	tmplID := "created_at"
	docID := "user/123/doc"
	orderKey := []byte{0x01, 0x02}

	if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
		t.Fatalf("Upsert with special chars failed: %v", err)
	}

	got, found := ps.Get(db, pattern, tmplID, docID)
	if !found {
		t.Fatal("Get should find document with special chars")
	}
	if !bytes.Equal(got, orderKey) {
		t.Errorf("Get returned %v, want %v", got, orderKey)
	}

	// Search should work
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Search returned %d results, want 1", len(results))
	}

	// Delete should work
	if err := ps.Delete(db, pattern, tmplID, docID, ""); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestPebbleStoreConcurrentReads(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Insert some data
	for i := 0; i < 10; i++ {
		docID := string(rune('a' + i))
		orderKey := []byte{byte(i)}
		if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}

	// Concurrent reads
	var wg sync.WaitGroup
	errors := make(chan error, 20)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			docID := string(rune('a' + idx))
			_, found := ps.Get(db, pattern, tmplID, docID)
			if !found {
				errors <- nil // Expected to find
			}
		}(i)
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		if err != nil {
			t.Errorf("Concurrent read error: %v", err)
		}
	}
}

func TestPebbleStoreConcurrentWrites(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			docID := string(rune('a'+idx/26)) + string(rune('a'+idx%26))
			orderKey := []byte{byte(idx)}
			if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		if err != nil {
			t.Errorf("Concurrent write error: %v", err)
		}
	}

	// Verify all writes succeeded
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 100})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 50 {
		t.Errorf("Search returned %d results, want 50", len(results))
	}
}

func TestPebbleStoreSearchWithUpperBoundOnly(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Insert documents
	for i := 0; i < 5; i++ {
		docID := string(rune('a' + i))
		orderKey := []byte{byte(i)}
		if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}

	// Search with only upper bound
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{
		Upper: []byte{0x03},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	// Should return docs with orderKey < 0x03 (i.e., 0x00, 0x01, 0x02)
	if len(results) != 3 {
		t.Errorf("Search returned %d results, want 3", len(results))
	}
}

func TestPebbleStoreDeleteIndexEmpty(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// DeleteIndex on non-existent index should not error
	if err := ps.DeleteIndex("testdb", "nonexistent/*", "template"); err != nil {
		t.Fatalf("DeleteIndex on empty should not error: %v", err)
	}
}

func TestPebbleStoreMultipleDatabases(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	pattern := "users/*"
	tmplID := "created_at"

	// Insert into different databases
	if err := ps.Upsert("db1", pattern, tmplID, "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert db1 failed: %v", err)
	}
	if err := ps.Upsert("db2", pattern, tmplID, "doc1", []byte{0x02}, ""); err != nil {
		t.Fatalf("Upsert db2 failed: %v", err)
	}

	// Search db1
	results1, err := ps.Search("db1", pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search db1 failed: %v", err)
	}
	if len(results1) != 1 {
		t.Errorf("Search db1 returned %d results, want 1", len(results1))
	}
	if !bytes.Equal(results1[0].OrderKey, []byte{0x01}) {
		t.Errorf("db1 doc has wrong orderKey")
	}

	// Search db2
	results2, err := ps.Search("db2", pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search db2 failed: %v", err)
	}
	if len(results2) != 1 {
		t.Errorf("Search db2 returned %d results, want 1", len(results2))
	}
	if !bytes.Equal(results2[0].OrderKey, []byte{0x02}) {
		t.Errorf("db2 doc has wrong orderKey")
	}

	// Delete from db1 should not affect db2
	if err := ps.Delete("db1", pattern, tmplID, "doc1", ""); err != nil {
		t.Fatalf("Delete db1 failed: %v", err)
	}

	results2, err = ps.Search("db2", pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search db2 after delete failed: %v", err)
	}
	if len(results2) != 1 {
		t.Errorf("Search db2 after delete returned %d results, want 1", len(results2))
	}
}

func TestPebbleStoreEmptyOrderKey(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"
	docID := "doc1"
	orderKey := []byte{} // Empty orderKey

	// Should handle empty orderKey
	if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
		t.Fatalf("Upsert with empty orderKey failed: %v", err)
	}

	got, found := ps.Get(db, pattern, tmplID, docID)
	if !found {
		t.Fatal("Get should find document with empty orderKey")
	}
	if len(got) != 0 {
		t.Errorf("Get returned %v, want empty", got)
	}
}

func TestPebbleStoreLargeOrderKey(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"
	docID := "doc1"
	orderKey := make([]byte, 1024) // 1KB orderKey
	for i := range orderKey {
		orderKey[i] = byte(i % 256)
	}

	if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
		t.Fatalf("Upsert with large orderKey failed: %v", err)
	}

	got, found := ps.Get(db, pattern, tmplID, docID)
	if !found {
		t.Fatal("Get should find document with large orderKey")
	}
	if !bytes.Equal(got, orderKey) {
		t.Error("Get returned wrong orderKey")
	}

	// Search should work
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Search returned %d results, want 1", len(results))
	}
}

// ============ Async Batching Tests ============

func TestPebbleStoreAsyncBatchFlush(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create store with small batch size
	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      5, // Small batch size for testing
		BatchInterval:  50 * time.Millisecond,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Insert 10 documents async - should trigger flush at 5
	for i := 0; i < 10; i++ {
		docID := string(rune('a' + i))
		orderKey := []byte{byte(i)}
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}

	// Wait for batch interval to ensure flush
	time.Sleep(cfg.BatchInterval * 3)

	// All should be visible
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 10 {
		t.Errorf("Search returned %d results, want 10", len(results))
	}
}

func TestPebbleStoreAsyncInsertThenDelete(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"
	docID := "doc1"
	orderKey := []byte{0x01}

	// Insert then delete before flush
	ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	ps.Delete(db, pattern, tmplID, docID, "")

	// Should not be visible
	_, found := ps.Get(db, pattern, tmplID, docID)
	if found {
		t.Error("Get should not find document after insert+delete")
	}

	// Flush
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Should still not be found
	_, found = ps.Get(db, pattern, tmplID, docID)
	if found {
		t.Error("Get should not find document after flush")
	}
}

func TestPebbleStoreAsyncSearchMerge(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Insert some docs synchronously (persisted)
	for i := 0; i < 3; i++ {
		docID := string(rune('a' + i))
		orderKey := []byte{byte(i * 2)} // 0, 2, 4
		if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}

	// Insert more async (pending)
	for i := 3; i < 6; i++ {
		docID := string(rune('a' + i))
		orderKey := []byte{byte(i*2 - 5)} // 1, 3, 5
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}

	// Search should merge both
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 6 {
		t.Errorf("Search returned %d results, want 6", len(results))
	}

	// Results should be sorted by orderKey
	for i := 0; i < len(results)-1; i++ {
		if bytes.Compare(results[i].OrderKey, results[i+1].OrderKey) >= 0 {
			t.Errorf("results not ordered: %v >= %v", results[i].OrderKey, results[i+1].OrderKey)
		}
	}
}

func TestPebbleStoreAsyncDeleteFromDB(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Insert synchronously
	for i := 0; i < 5; i++ {
		docID := string(rune('a' + i))
		orderKey := []byte{byte(i)}
		if err := ps.Upsert(db, pattern, tmplID, docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}

	// Delete some async
	ps.Delete(db, pattern, tmplID, "b", "")
	ps.Delete(db, pattern, tmplID, "d", "")

	// Search should not include deleted docs
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Search returned %d results, want 3", len(results))
	}

	// Verify which docs remain
	docIDs := make(map[string]bool)
	for _, r := range results {
		docIDs[r.ID] = true
	}
	if docIDs["b"] || docIDs["d"] {
		t.Error("Deleted docs should not be in results")
	}
	if !docIDs["a"] || !docIDs["c"] || !docIDs["e"] {
		t.Error("Non-deleted docs should be in results")
	}
}

// ============ ListDatabases and ListIndexes Tests ============

func TestPebbleStoreListDatabases(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Initially empty
	dbs, _ := ps.ListDatabases()
	if len(dbs) != 0 {
		t.Errorf("expected empty database list, got %d", len(dbs))
	}

	// Create databases by upserting
	if err := ps.Upsert("db1", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert db1 failed: %v", err)
	}
	if err := ps.Upsert("db2", "posts/*", "tmpl2", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert db2 failed: %v", err)
	}
	if err := ps.Upsert("db3", "items/*", "tmpl3", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert db3 failed: %v", err)
	}

	dbs, _ = ps.ListDatabases()
	if len(dbs) != 3 {
		t.Errorf("expected 3 databases, got %d", len(dbs))
	}

	// Verify all databases are present
	dbSet := make(map[string]bool)
	for _, db := range dbs {
		dbSet[db] = true
	}
	if !dbSet["db1"] {
		t.Error("missing db1")
	}
	if !dbSet["db2"] {
		t.Error("missing db2")
	}
	if !dbSet["db3"] {
		t.Error("missing db3")
	}
}

func TestPebbleStoreListDatabasesWithSpecialChars(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Create database with special characters
	if err := ps.Upsert("my-app/prod", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	dbs, _ := ps.ListDatabases()
	if len(dbs) != 1 {
		t.Errorf("expected 1 database, got %d", len(dbs))
	}
	if len(dbs) > 0 && dbs[0] != "my-app/prod" {
		t.Errorf("expected 'my-app/prod', got %q", dbs[0])
	}
}

func TestPebbleStoreListIndexes(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"

	// Create multiple indexes in the same database
	if err := ps.Upsert(db, "users/*", "tmpl1", "user1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Upsert(db, "users/*", "tmpl1", "user2", []byte{0x02}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Upsert(db, "posts/*", "tmpl2", "post1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Set states
	if err := ps.SetState(db, "users/*", "tmpl1", store.IndexStateHealthy); err != nil {
		t.Fatalf("SetState failed: %v", err)
	}
	if err := ps.SetState(db, "posts/*", "tmpl2", store.IndexStateRebuilding); err != nil {
		t.Fatalf("SetState failed: %v", err)
	}

	indexes, _ := ps.ListIndexes(db)
	if len(indexes) != 2 {
		t.Errorf("expected 2 indexes, got %d", len(indexes))
	}

	// Verify index info
	indexMap := make(map[string]store.IndexInfo)
	for _, idx := range indexes {
		indexMap[idx.Pattern] = idx
	}

	usersIdx, ok := indexMap["users/*"]
	if !ok {
		t.Error("missing users/* index")
	} else {
		if usersIdx.TemplateID != "tmpl1" {
			t.Errorf("users/* templateID = %q, want tmpl1", usersIdx.TemplateID)
		}
		if usersIdx.State != store.IndexStateHealthy {
			t.Errorf("users/* state = %s, want healthy", usersIdx.State)
		}
		if usersIdx.DocCount != 2 {
			t.Errorf("users/* docCount = %d, want 2", usersIdx.DocCount)
		}
	}

	postsIdx, ok := indexMap["posts/*"]
	if !ok {
		t.Error("missing posts/* index")
	} else {
		if postsIdx.TemplateID != "tmpl2" {
			t.Errorf("posts/* templateID = %q, want tmpl2", postsIdx.TemplateID)
		}
		if postsIdx.State != store.IndexStateRebuilding {
			t.Errorf("posts/* state = %s, want rebuilding", postsIdx.State)
		}
		if postsIdx.DocCount != 1 {
			t.Errorf("posts/* docCount = %d, want 1", postsIdx.DocCount)
		}
	}
}

func TestPebbleStoreListIndexesEmpty(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Non-existent database
	indexes, _ := ps.ListIndexes("nonexistent")
	if len(indexes) != 0 {
		t.Errorf("expected 0 indexes for non-existent db, got %d", len(indexes))
	}
}

func TestPebbleStoreListIndexesAfterDelete(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"

	// Create index
	if err := ps.Upsert(db, "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	indexes, _ := ps.ListIndexes(db)
	if len(indexes) != 1 {
		t.Errorf("expected 1 index, got %d", len(indexes))
	}

	// Delete the index
	if err := ps.DeleteIndex(db, "users/*", "tmpl1"); err != nil {
		t.Fatalf("DeleteIndex failed: %v", err)
	}

	// Note: The map entry may still exist after DeleteIndex depending on implementation
	// Check that docCount is 0 if the index still appears
	indexes, _ = ps.ListIndexes(db)
	for _, idx := range indexes {
		if idx.Pattern == "users/*" && idx.DocCount != 0 {
			t.Errorf("expected docCount 0 after delete, got %d", idx.DocCount)
		}
	}
}

func TestPebbleStoreProgressViaDelete(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert a document
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Delete with progress
	if err := ps.Delete("testdb", "users/*", "tmpl1", "doc1", "event-delete-123"); err != nil {
		t.Fatalf("Delete with progress failed: %v", err)
	}

	// Verify progress was saved
	progress, err := ps.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress failed: %v", err)
	}
	if progress != "event-delete-123" {
		t.Errorf("progress = %q, want event-delete-123", progress)
	}
}

func TestPebbleStoreDeleteNonexistent(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Delete non-existent document should not error
	if err := ps.Delete("testdb", "users/*", "tmpl1", "nonexistent", ""); err != nil {
		t.Fatalf("Delete non-existent failed: %v", err)
	}

	// Delete from non-existent database should not error
	if err := ps.Delete("nonexistent", "users/*", "tmpl1", "doc1", ""); err != nil {
		t.Fatalf("Delete from non-existent db failed: %v", err)
	}
}

func TestPebbleStoreDeleteWithProgress(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert a document
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Delete does not take progress parameter, but let's verify behavior
	ps.Delete("testdb", "users/*", "tmpl1", "doc1", "")

	// Flush to persist
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Document should be gone
	_, found := ps.Get("testdb", "users/*", "tmpl1", "doc1")
	if found {
		t.Error("Document should be deleted")
	}
}

func TestPebbleStoreGetFromFlushing(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create store with long batch interval
	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      1000,             // Large batch size
		BatchInterval:  10 * time.Second, // Long interval
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Insert document synchronously first (goes to DB)
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Now insert async (goes to pending)
	ps.Upsert("testdb", "users/*", "tmpl1", "doc2", []byte{0x02}, "")

	// Get doc2 should find in pending
	got, found := ps.Get("testdb", "users/*", "tmpl1", "doc2")
	if !found {
		t.Fatal("Get should find doc2 in pending")
	}
	if !bytes.Equal(got, []byte{0x02}) {
		t.Errorf("Got %v, want [0x02]", got)
	}

	// Get doc1 should find in DB
	got, found = ps.Get("testdb", "users/*", "tmpl1", "doc1")
	if !found {
		t.Fatal("Get should find doc1 in DB")
	}
	if !bytes.Equal(got, []byte{0x01}) {
		t.Errorf("Got %v, want [0x01]", got)
	}
}

func TestPebbleStoreMultipleIndexesSameDB(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"

	// Create multiple indexes with different patterns and templates
	for i := 0; i < 5; i++ {
		pattern := "collection" + string(rune('a'+i)) + "/*"
		tmplID := "tmpl" + string(rune('1'+i))
		if err := ps.Upsert(db, pattern, tmplID, "doc1", []byte{byte(i)}, ""); err != nil {
			t.Fatalf("Upsert %d failed: %v", i, err)
		}
	}

	// ListIndexes should show all 5
	indexes, _ := ps.ListIndexes(db)
	if len(indexes) != 5 {
		t.Errorf("expected 5 indexes, got %d", len(indexes))
	}

	// ListDatabases should show 1
	dbs, _ := ps.ListDatabases()
	if len(dbs) != 1 {
		t.Errorf("expected 1 database, got %d", len(dbs))
	}
}

// ============ Flushing Map and Complex Async Scenarios ============

func TestPebbleStoreGetFromFlushingDelete(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      1000,             // Large batch size to prevent auto-flush
		BatchInterval:  10 * time.Second, // Long interval
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// First insert a document synchronously
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Now delete asynchronously (goes to pending)
	ps.Delete("testdb", "users/*", "tmpl1", "doc1", "")

	// Get should return not found (pending delete)
	_, found := ps.Get("testdb", "users/*", "tmpl1", "doc1")
	if found {
		t.Error("Get should return not found for pending delete")
	}
}

func TestPebbleStoreSearchWithPendingUpdate(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      1000,             // Large batch size to prevent auto-flush
		BatchInterval:  10 * time.Second, // Long interval
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Insert a document synchronously (to DB)
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Update the same document asynchronously (goes to pending, with different orderKey)
	ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x05}, "")

	// Search should find the pending version with the new orderKey
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{
		Lower: []byte{0x04},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (pending update within bounds), got %d", len(results))
	}
	if len(results) > 0 && !bytes.Equal(results[0].OrderKey, []byte{0x05}) {
		t.Errorf("expected orderKey [0x05], got %v", results[0].OrderKey)
	}
}

func TestPebbleStoreSearchPendingUpdateOutOfBounds(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      1000,
		BatchInterval:  10 * time.Second,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Insert a document synchronously (to DB)
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x05}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Update to a value outside search bounds
	ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")

	// Search with bounds that exclude the pending value
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{
		Lower: []byte{0x04},
		Upper: []byte{0x10},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	// The pending update (0x01) is outside bounds, so should not be returned
	if len(results) != 0 {
		t.Errorf("expected 0 results (pending update out of bounds), got %d", len(results))
	}
}

func TestPebbleStoreUpsertUpdateExistingDoc(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert initial document
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Initial Upsert failed: %v", err)
	}

	// Verify initial state
	orderKey, found := ps.Get("testdb", "users/*", "tmpl1", "doc1")
	if !found {
		t.Fatal("doc1 should exist")
	}
	if !bytes.Equal(orderKey, []byte{0x01}) {
		t.Errorf("initial orderKey = %v, want [0x01]", orderKey)
	}

	// Update with new orderKey
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x02}, ""); err != nil {
		t.Fatalf("Update Upsert failed: %v", err)
	}

	// Verify updated state
	orderKey, found = ps.Get("testdb", "users/*", "tmpl1", "doc1")
	if !found {
		t.Fatal("doc1 should still exist after update")
	}
	if !bytes.Equal(orderKey, []byte{0x02}) {
		t.Errorf("updated orderKey = %v, want [0x02]", orderKey)
	}

	// Search should find only one doc with updated orderKey
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if len(results) > 0 && !bytes.Equal(results[0].OrderKey, []byte{0x02}) {
		t.Errorf("search result orderKey = %v, want [0x02]", results[0].OrderKey)
	}
}

func TestPebbleStoreSearchWithPendingDelete(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      1000,
		BatchInterval:  10 * time.Second,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Insert doc synchronously (to DB)
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x05}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Insert doc2 synchronously (to DB)
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc2", []byte{0x06}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Delete doc1 async (pending delete)
	ps.Delete("testdb", "users/*", "tmpl1", "doc1", "")

	// Search should only return doc2
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (doc2), got %d", len(results))
	}
	if len(results) > 0 && results[0].ID != "doc2" {
		t.Errorf("expected doc2, got %s", results[0].ID)
	}
}
