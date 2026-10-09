package persist_store

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/indexer/config"
	"github.com/codetreker/syntrix/internal/indexer/store"
)

// ============ Coverage-specific Tests ============

// TestApplyOpDeleteExistingDoc covers the delete path in applyOp when doc exists in DB
func TestApplyOpDeleteExistingDoc(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert document and flush to persist to DB
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Verify doc exists in DB
	orderKey, found := ps.Get("testdb", "users/*", "tmpl1", "doc1")
	if !found {
		t.Fatal("doc1 should exist after flush")
	}
	if !bytes.Equal(orderKey, []byte{0x01}) {
		t.Errorf("orderKey = %v, want [0x01]", orderKey)
	}

	// Now delete (this will go through applyOp delete path where doc exists in DB)
	if err := ps.Delete("testdb", "users/*", "tmpl1", "doc1", ""); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Verify doc is gone
	_, found = ps.Get("testdb", "users/*", "tmpl1", "doc1")
	if found {
		t.Error("doc1 should be deleted")
	}
}

// TestApplyOpUpsertExistingDoc covers the upsert path in applyOp when updating existing doc
func TestApplyOpUpsertExistingDoc(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert document and flush to persist to DB
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Now update with different orderKey (this will go through applyOp upsert path with existing doc)
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x02}, ""); err != nil {
		t.Fatalf("Update Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Verify doc has new orderKey
	orderKey, found := ps.Get("testdb", "users/*", "tmpl1", "doc1")
	if !found {
		t.Fatal("doc1 should exist after update")
	}
	if !bytes.Equal(orderKey, []byte{0x02}) {
		t.Errorf("orderKey = %v, want [0x02]", orderKey)
	}

	// Search should only find one doc with updated orderKey
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

// TestSearchFastPathStartAfter covers the StartAfter cursor handling in searchFastPath
func TestSearchFastPathStartAfter(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert documents and flush to ensure no pending ops
	for i := 0; i < 5; i++ {
		docID := string(rune('a' + i))
		orderKey := []byte{byte(i)}
		if err := ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Search with StartAfter to trigger cursor skip logic in fast path
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{
		StartAfter: []byte{0x01}, // Skip docs with orderKey <= 0x01
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	// Should return docs c, d, e (orderKeys 0x02, 0x03, 0x04)
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	if len(results) > 0 && results[0].ID != "c" {
		t.Errorf("expected first result to be 'c', got %s", results[0].ID)
	}
}

// TestSearchWithPendingStartAfter covers StartAfter in merge path
func TestSearchWithPendingStartAfter(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert to DB
	for i := 0; i < 3; i++ {
		docID := "db" + string(rune('a'+i))
		orderKey := []byte{byte(i * 2)} // 0x00, 0x02, 0x04
		if err := ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Insert pending ops
	for i := 0; i < 3; i++ {
		docID := "pend" + string(rune('a'+i))
		orderKey := []byte{byte(i*2 + 1)} // 0x01, 0x03, 0x05
		ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, "")
	}

	// Search with StartAfter should skip cursor in merge path
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{
		StartAfter: []byte{0x02}, // Skip docs with orderKey <= 0x02
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	// Should return: penda(0x03), dbc(0x04), pendb(0x05) - 3 results after cursor
	if len(results) < 3 {
		t.Errorf("expected at least 3 results after cursor, got %d", len(results))
	}
}

// TestSearchWithPendingMergeBothSources covers the merge when both DB and pending have docs
func TestSearchWithPendingMergeBothSources(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert to DB with even keys
	for i := 0; i < 5; i++ {
		docID := "db" + string(rune('a'+i))
		orderKey := []byte{byte(i * 2)} // 0, 2, 4, 6, 8
		if err := ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Insert pending with odd keys
	for i := 0; i < 5; i++ {
		docID := "pend" + string(rune('a'+i))
		orderKey := []byte{byte(i*2 + 1)} // 1, 3, 5, 7, 9
		ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, "")
	}

	// Search should merge both and return in order
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 20})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 10 {
		t.Errorf("expected 10 results, got %d", len(results))
	}

	// Verify ordering
	for i := 0; i < len(results)-1; i++ {
		if bytes.Compare(results[i].OrderKey, results[i+1].OrderKey) >= 0 {
			t.Errorf("results not ordered at index %d: %v >= %v", i, results[i].OrderKey, results[i+1].OrderKey)
		}
	}
}

// TestSearchWithPendingOnlyPending covers merge when DB is empty
func TestSearchWithPendingOnlyPending(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Only insert pending ops, no DB data
	for i := 0; i < 5; i++ {
		docID := "pend" + string(rune('a'+i))
		orderKey := []byte{byte(i)}
		ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, "")
	}

	// Search should return all pending
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("expected 5 results, got %d", len(results))
	}
}

// TestSearchWithPendingOnlyDB covers merge when pending is empty (should use fast path)
func TestSearchWithPendingOnlyDB(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert to DB and flush
	for i := 0; i < 5; i++ {
		docID := "db" + string(rune('a'+i))
		orderKey := []byte{byte(i)}
		if err := ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Search with no pending should use fast path
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("expected 5 results, got %d", len(results))
	}
}

// TestSearchWithPendingExhaustDB covers merge when DB exhausts before pending
func TestSearchWithPendingExhaustDB(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert 2 docs to DB
	for i := 0; i < 2; i++ {
		docID := "db" + string(rune('a'+i))
		orderKey := []byte{byte(i)} // 0, 1
		if err := ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Insert 5 pending docs with higher keys
	for i := 0; i < 5; i++ {
		docID := "pend" + string(rune('a'+i))
		orderKey := []byte{byte(i + 10)} // 10, 11, 12, 13, 14
		ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, "")
	}

	// Search should exhaust DB first, then continue with pending
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 7 {
		t.Errorf("expected 7 results, got %d", len(results))
	}
}

// TestSearchWithPendingExhaustPending covers merge when pending exhausts before DB
func TestSearchWithPendingExhaustPending(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert 5 docs to DB with higher keys
	for i := 0; i < 5; i++ {
		docID := "db" + string(rune('a'+i))
		orderKey := []byte{byte(i + 10)} // 10, 11, 12, 13, 14
		if err := ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Insert 2 pending docs with lower keys
	for i := 0; i < 2; i++ {
		docID := "pend" + string(rune('a'+i))
		orderKey := []byte{byte(i)} // 0, 1
		ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, "")
	}

	// Search should exhaust pending first, then continue with DB
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 7 {
		t.Errorf("expected 7 results, got %d", len(results))
	}

	// Verify order: pending first (0, 1), then DB (10, 11, 12, 13, 14)
	if len(results) >= 2 {
		if results[0].OrderKey[0] != 0 || results[1].OrderKey[0] != 1 {
			t.Errorf("first two results should be from pending")
		}
	}
}

// TestSearchWithPendingLimit covers limit being reached during merge
func TestSearchWithPendingLimit(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert 10 docs to DB
	for i := 0; i < 10; i++ {
		docID := "db" + string(rune('a'+i))
		orderKey := []byte{byte(i * 2)} // 0, 2, 4, ...
		if err := ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Insert 10 pending docs
	for i := 0; i < 10; i++ {
		docID := "pend" + string(rune('a'+i))
		orderKey := []byte{byte(i*2 + 1)} // 1, 3, 5, ...
		ps.Upsert("testdb", "users/*", "tmpl1", docID, orderKey, "")
	}

	// Search with limit should stop at limit
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("expected 5 results (limit), got %d", len(results))
	}
}

// TestListDatabasesWithPersistedData tests ListDatabases with persisted data
func TestListDatabasesWithPersistedData(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert and flush to persist
	if err := ps.Upsert("db1", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert db1 failed: %v", err)
	}
	if err := ps.Upsert("db2", "posts/*", "tmpl2", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert db2 failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Add a pending one
	ps.Upsert("db3", "items/*", "tmpl3", "doc1", []byte{0x01}, "")

	dbs, _ := ps.ListDatabases()
	if len(dbs) != 3 {
		t.Errorf("expected 3 databases, got %d", len(dbs))
	}
}

// TestListIndexesWithPersistedData tests ListIndexes with persisted data
func TestListIndexesWithPersistedData(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"

	// Insert and flush to persist
	if err := ps.Upsert(db, "users/*", "tmpl1", "user1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Upsert(db, "posts/*", "tmpl2", "post1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Add a pending one
	ps.Upsert(db, "items/*", "tmpl3", "item1", []byte{0x01}, "")

	indexes, _ := ps.ListIndexes(db)
	if len(indexes) != 3 {
		t.Errorf("expected 3 indexes, got %d", len(indexes))
	}
}

// TestListIndexesPendingDelete covers filtering of pending deletes
func TestListIndexesPendingDelete(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"

	// Create index and flush
	if err := ps.Upsert(db, "users/*", "tmpl1", "user1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Mark index for deletion
	if err := ps.DeleteIndex(db, "users/*", "tmpl1"); err != nil {
		t.Fatalf("DeleteIndex failed: %v", err)
	}

	// ListIndexes should not include the pending delete index
	indexes, _ := ps.ListIndexes(db)
	for _, idx := range indexes {
		if idx.Pattern == "users/*" && idx.TemplateID == "tmpl1" {
			t.Error("ListIndexes should not include pending delete index")
		}
	}
}

// TestGetFromFlushingMap covers Get reading from flushing map
func TestGetFromFlushingMap(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      1, // Very small batch size to trigger flush
		BatchInterval:  50 * time.Millisecond,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Insert doc to trigger flush
	ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")

	// Immediately insert another doc (first one may be flushing)
	ps.Upsert("testdb", "users/*", "tmpl1", "doc2", []byte{0x02}, "")

	// Get both docs - one may be in flushing, one in pending
	got1, found1 := ps.Get("testdb", "users/*", "tmpl1", "doc1")
	got2, found2 := ps.Get("testdb", "users/*", "tmpl1", "doc2")

	if !found1 || !found2 {
		t.Errorf("both docs should be found: doc1=%v, doc2=%v", found1, found2)
	}
	if found1 && !bytes.Equal(got1, []byte{0x01}) {
		t.Errorf("doc1 orderKey = %v, want [0x01]", got1)
	}
	if found2 && !bytes.Equal(got2, []byte{0x02}) {
		t.Errorf("doc2 orderKey = %v, want [0x02]", got2)
	}
}

// TestGetFromFlushingMapDelete covers Get returning not found for delete in flushing
func TestGetFromFlushingMapDelete(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      1,
		BatchInterval:  50 * time.Millisecond,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Insert and flush
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Delete to trigger flush
	ps.Delete("testdb", "users/*", "tmpl1", "doc1", "")

	// Insert another to potentially move delete to flushing
	ps.Upsert("testdb", "users/*", "tmpl1", "doc2", []byte{0x02}, "")

	// Get doc1 should not be found (pending or flushing delete)
	_, found := ps.Get("testdb", "users/*", "tmpl1", "doc1")
	if found {
		t.Error("doc1 should not be found after delete")
	}
}

// TestUpsertDuringPendingIndexDelete covers upsert being skipped during index delete
func TestUpsertDuringPendingIndexDelete(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      1000, // Large to prevent auto-flush
		BatchInterval:  10 * time.Second,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Queue index delete
	if err := ps.DeleteIndex("testdb", "users/*", "tmpl1"); err != nil {
		t.Fatalf("DeleteIndex failed: %v", err)
	}

	// Upsert during pending delete should be skipped but progress saved
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "progress-123"); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	// Progress should be saved
	progress, err := ps.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress failed: %v", err)
	}
	if progress != "progress-123" {
		t.Errorf("progress = %q, want progress-123", progress)
	}

	// Search for the index should return empty (index deleted + upsert skipped)
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results during pending delete, got %d", len(results))
	}
}

// TestDeleteDuringPendingIndexDelete covers delete being skipped during index delete
func TestDeleteDuringPendingIndexDelete(t *testing.T) {
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

	// Queue index delete
	if err := ps.DeleteIndex("testdb", "users/*", "tmpl1"); err != nil {
		t.Fatalf("DeleteIndex failed: %v", err)
	}

	// Delete during pending delete should be skipped but progress saved
	if err := ps.Delete("testdb", "users/*", "tmpl1", "doc1", "progress-456"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Progress should be saved
	progress, err := ps.LoadProgress()
	if err != nil {
		t.Fatalf("LoadProgress failed: %v", err)
	}
	if progress != "progress-456" {
		t.Errorf("progress = %q, want progress-456", progress)
	}
}

// TestGetDuringPendingIndexDelete covers Get returning not found during index delete
func TestGetDuringPendingIndexDelete(t *testing.T) {
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

	// Insert and flush first
	if err := ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Queue index delete
	if err := ps.DeleteIndex("testdb", "users/*", "tmpl1"); err != nil {
		t.Fatalf("DeleteIndex failed: %v", err)
	}

	// Get should return not found during pending index delete
	_, found := ps.Get("testdb", "users/*", "tmpl1", "doc1")
	if found {
		t.Error("Get should return not found during pending index delete")
	}
}

// TestSearchDuringPendingIndexDelete covers Search returning empty during index delete
func TestSearchDuringPendingIndexDelete(t *testing.T) {
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

	// Insert and flush
	for i := 0; i < 5; i++ {
		docID := string(rune('a' + i))
		if err := ps.Upsert("testdb", "users/*", "tmpl1", docID, []byte{byte(i)}, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Queue index delete
	if err := ps.DeleteIndex("testdb", "users/*", "tmpl1"); err != nil {
		t.Fatalf("DeleteIndex failed: %v", err)
	}

	// Search should return empty during pending index delete
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results during pending index delete, got %d", len(results))
	}
}

// TestGetFromFlushingWithOrderKey covers Get reading from flushing with non-nil orderKey
func TestGetFromFlushingWithOrderKey(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      2, // Trigger flush after 2 ops
		BatchInterval:  50 * time.Millisecond,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Insert 2 ops to trigger flush (they will move to flushing)
	ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	ps.Upsert("testdb", "users/*", "tmpl1", "doc2", []byte{0x02}, "")

	// Immediately read - doc1 might be in flushing or DB, doc2 in pending or flushing
	got1, found1 := ps.Get("testdb", "users/*", "tmpl1", "doc1")
	got2, found2 := ps.Get("testdb", "users/*", "tmpl1", "doc2")

	if !found1 || !found2 {
		t.Errorf("both docs should be found: doc1=%v, doc2=%v", found1, found2)
	}
	if found1 && !bytes.Equal(got1, []byte{0x01}) {
		t.Errorf("doc1 orderKey = %v, want [0x01]", got1)
	}
	if found2 && !bytes.Equal(got2, []byte{0x02}) {
		t.Errorf("doc2 orderKey = %v, want [0x02]", got2)
	}
}

// TestListDatabasesFromFlushing covers ListDatabases reading from flushing map
func TestListDatabasesFromFlushing(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      2, // Trigger flush after 2 ops
		BatchInterval:  50 * time.Millisecond,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Insert to different dbs to trigger flush
	ps.Upsert("db1", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	ps.Upsert("db2", "posts/*", "tmpl2", "doc1", []byte{0x01}, "")

	// Immediately check - data might be in flushing
	dbs, _ := ps.ListDatabases()
	if len(dbs) < 2 {
		t.Errorf("expected at least 2 databases, got %d", len(dbs))
	}
}

// TestListIndexesFromFlushing covers ListIndexes reading from flushing map
func TestListIndexesFromFlushing(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      2, // Trigger flush after 2 ops
		BatchInterval:  50 * time.Millisecond,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	db := "testdb"

	// Insert to different indexes to trigger flush
	ps.Upsert(db, "users/*", "tmpl1", "user1", []byte{0x01}, "")
	ps.Upsert(db, "posts/*", "tmpl2", "post1", []byte{0x01}, "")

	// Immediately check - data might be in flushing
	indexes, _ := ps.ListIndexes(db)
	if len(indexes) < 2 {
		t.Errorf("expected at least 2 indexes, got %d", len(indexes))
	}
}

// TestListDatabasesOnlyDeletesInFlushing covers ListDatabases skipping deletes in flushing
func TestListDatabasesOnlyDeletesInFlushing(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      2,
		BatchInterval:  50 * time.Millisecond,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Insert and flush first
	if err := ps.Upsert("db1", "users/*", "tmpl1", "doc1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Now delete to trigger flush (delete goes to flushing)
	ps.Delete("db1", "users/*", "tmpl1", "doc1", "")
	ps.Delete("db1", "users/*", "tmpl1", "doc2", "") // Another to trigger flush

	// ListDatabases should still show db1 from persisted data
	// (deletes in flushing have orderKey=nil and should be skipped)
	dbs, _ := ps.ListDatabases()
	foundDB1 := false
	for _, db := range dbs {
		if db == "db1" {
			foundDB1 = true
			break
		}
	}
	if !foundDB1 {
		t.Error("db1 should still be in list (from persisted data)")
	}
}

// TestListIndexesOnlyDeletesInFlushing covers ListIndexes skipping deletes in flushing
func TestListIndexesOnlyDeletesInFlushing(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      2,
		BatchInterval:  50 * time.Millisecond,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	db := "testdb"

	// Insert and flush first
	if err := ps.Upsert(db, "users/*", "tmpl1", "user1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Now delete to trigger flush
	ps.Delete(db, "users/*", "tmpl1", "user1", "")
	ps.Delete(db, "users/*", "tmpl1", "user2", "")

	// ListIndexes should still show users/* from persisted data
	indexes, _ := ps.ListIndexes(db)
	foundUsers := false
	for _, idx := range indexes {
		if idx.Pattern == "users/*" {
			foundUsers = true
			break
		}
	}
	if !foundUsers {
		t.Error("users/* index should still be in list (from persisted data)")
	}
}

// TestSearchFromFlushing covers Search reading from flushing map
func TestSearchFromFlushing(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      3,
		BatchInterval:  50 * time.Millisecond,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	// Insert 3 ops to trigger flush
	ps.Upsert("testdb", "users/*", "tmpl1", "doc1", []byte{0x01}, "")
	ps.Upsert("testdb", "users/*", "tmpl1", "doc2", []byte{0x02}, "")
	ps.Upsert("testdb", "users/*", "tmpl1", "doc3", []byte{0x03}, "")

	// Immediately search - data might be in flushing
	results, err := ps.Search("testdb", "users/*", "tmpl1", store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) < 3 {
		t.Errorf("expected at least 3 results, got %d", len(results))
	}
}

// TestCountDocsFromFlushing covers countDocs reading from flushing map
func TestCountDocsFromFlushing(t *testing.T) {
	t.Parallel()
	tmpDir, err := os.MkdirTemp("", "pebble-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "test.db"),
		BatchSize:      2,
		BatchInterval:  50 * time.Millisecond,
		BlockCacheSize: 8 * 1024 * 1024,
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewPebbleStore failed: %v", err)
	}
	defer ps.Close()

	db := "testdb"

	// Insert to trigger flush
	ps.Upsert(db, "users/*", "tmpl1", "user1", []byte{0x01}, "")
	ps.Upsert(db, "users/*", "tmpl1", "user2", []byte{0x02}, "")

	// Immediately check - data might be in flushing
	indexes, _ := ps.ListIndexes(db)
	if len(indexes) != 1 {
		t.Fatalf("expected 1 index, got %d", len(indexes))
	}
	if indexes[0].DocCount < 2 {
		t.Errorf("expected docCount >= 2, got %d", indexes[0].DocCount)
	}
}

// TestGetFromFlushingDirect directly manipulates flushing map to guarantee coverage
func TestGetFromFlushingDirect(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "tmpl1"
	docID := "doc1"

	// Manually place data in flushing map
	hash := indexHash(pattern, tmplID)
	idxMapKey := db + "|" + hash

	ps.mu.Lock()
	ps.flushing[idxMapKey] = map[string]*pendingOp{
		docID: {
			db:       db,
			pattern:  pattern,
			tmplID:   tmplID,
			hash:     hash,
			docID:    docID,
			orderKey: []byte{0x42}, // non-nil = upsert
		},
	}
	ps.mu.Unlock()

	// Get should find it in flushing
	got, found := ps.Get(db, pattern, tmplID, docID)
	if !found {
		t.Error("Get should find document in flushing")
	}
	if !bytes.Equal(got, []byte{0x42}) {
		t.Errorf("orderKey = %v, want [0x42]", got)
	}

	// Clean up
	ps.mu.Lock()
	delete(ps.flushing, idxMapKey)
	ps.mu.Unlock()
}

// TestGetFromFlushingDeleteDirect tests flushing delete path
func TestGetFromFlushingDeleteDirect(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "tmpl1"
	docID := "doc1"

	hash := indexHash(pattern, tmplID)
	idxMapKey := db + "|" + hash

	// Manually place delete in flushing map
	ps.mu.Lock()
	ps.flushing[idxMapKey] = map[string]*pendingOp{
		docID: {
			db:       db,
			pattern:  pattern,
			tmplID:   tmplID,
			hash:     hash,
			docID:    docID,
			orderKey: nil, // nil = delete
		},
	}
	ps.mu.Unlock()

	// Get should return not found for flushing delete
	_, found := ps.Get(db, pattern, tmplID, docID)
	if found {
		t.Error("Get should return not found for flushing delete")
	}

	// Clean up
	ps.mu.Lock()
	delete(ps.flushing, idxMapKey)
	ps.mu.Unlock()
}

// TestListDatabasesFromFlushingDirect directly manipulates flushing map
func TestListDatabasesFromFlushingDirect(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "flushdb"
	pattern := "users/*"
	tmplID := "tmpl1"
	hash := indexHash(pattern, tmplID)
	idxMapKey := db + "|" + hash

	// Manually place data in flushing map
	ps.mu.Lock()
	ps.flushing[idxMapKey] = map[string]*pendingOp{
		"doc1": {
			db:       db,
			pattern:  pattern,
			tmplID:   tmplID,
			hash:     hash,
			docID:    "doc1",
			orderKey: []byte{0x01}, // upsert
		},
	}
	ps.mu.Unlock()

	// ListDatabases should include flushdb from flushing
	dbs, _ := ps.ListDatabases()
	foundDB := false
	for _, d := range dbs {
		if d == db {
			foundDB = true
			break
		}
	}
	if !foundDB {
		t.Errorf("expected to find %s in ListDatabases from flushing", db)
	}

	// Clean up
	ps.mu.Lock()
	delete(ps.flushing, idxMapKey)
	ps.mu.Unlock()
}

// TestListIndexesFromFlushingDirect directly manipulates flushing map
func TestListIndexesFromFlushingDirect(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "flushpattern/*"
	tmplID := "flushtmpl"
	hash := indexHash(pattern, tmplID)
	idxMapKey := db + "|" + hash

	// Manually place data in flushing map
	ps.mu.Lock()
	ps.flushing[idxMapKey] = map[string]*pendingOp{
		"doc1": {
			db:       db,
			pattern:  pattern,
			tmplID:   tmplID,
			hash:     hash,
			docID:    "doc1",
			orderKey: []byte{0x01}, // upsert
		},
	}
	ps.mu.Unlock()

	// ListIndexes should include the flushing index
	indexes, _ := ps.ListIndexes(db)
	foundIdx := false
	for _, idx := range indexes {
		if idx.Pattern == pattern && idx.TemplateID == tmplID {
			foundIdx = true
			break
		}
	}
	if !foundIdx {
		t.Errorf("expected to find %s/%s in ListIndexes from flushing", pattern, tmplID)
	}

	// Clean up
	ps.mu.Lock()
	delete(ps.flushing, idxMapKey)
	ps.mu.Unlock()
}

// TestSearchFromFlushingDirect directly manipulates flushing map
func TestSearchFromFlushingDirect(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "tmpl1"
	hash := indexHash(pattern, tmplID)
	idxMapKey := db + "|" + hash

	// Manually place data in flushing map
	ps.mu.Lock()
	ps.flushing[idxMapKey] = map[string]*pendingOp{
		"doc1": {
			db:       db,
			pattern:  pattern,
			tmplID:   tmplID,
			hash:     hash,
			docID:    "doc1",
			orderKey: []byte{0x01},
		},
		"doc2": {
			db:       db,
			pattern:  pattern,
			tmplID:   tmplID,
			hash:     hash,
			docID:    "doc2",
			orderKey: []byte{0x02},
		},
	}
	ps.mu.Unlock()

	// Search should find flushing docs
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) < 2 {
		t.Errorf("expected at least 2 results from flushing, got %d", len(results))
	}

	// Clean up
	ps.mu.Lock()
	delete(ps.flushing, idxMapKey)
	ps.mu.Unlock()
}

// TestCountDocsWithPending covers countDocs considering pending ops
func TestCountDocsWithPending(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "tmpl1"

	// Insert to DB and flush
	for i := 0; i < 3; i++ {
		docID := "db" + string(rune('a'+i))
		if err := ps.Upsert(db, pattern, tmplID, docID, []byte{byte(i)}, ""); err != nil {
			t.Fatalf("Upsert failed: %v", err)
		}
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Insert pending (including one that will be a delete)
	ps.Upsert(db, pattern, tmplID, "penda", []byte{0x10}, "")
	ps.Delete(db, pattern, tmplID, "dba", "") // Delete one from DB

	// ListIndexes should count correctly
	indexes, _ := ps.ListIndexes(db)
	if len(indexes) != 1 {
		t.Fatalf("expected 1 index, got %d", len(indexes))
	}
	// dbA deleted, dbB, dbC remain, pendA added = 3 docs
	if indexes[0].DocCount != 3 {
		t.Errorf("expected docCount=3, got %d", indexes[0].DocCount)
	}
}

// ============ Error Path Tests ============
// Note: Many error paths in pebble.go require the underlying PebbleDB to return errors.
// Pebble panics when the DB is closed and methods are called, so we cannot easily
// test those paths without proper mocking. The tests below cover what we can test.

// TestApplyOpUpsertWithExistingDocFlush tests applyOp upsert path when document exists.
// This specifically tests lines 391-397 where oldOrderKey is found.
func TestApplyOpUpsertWithExistingDocFlush(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "tmpl1"
	docID := "doc1"

	// Insert first version
	if err := ps.Upsert(db, pattern, tmplID, docID, []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert 1 failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush 1 failed: %v", err)
	}

	// Update with new orderKey - this triggers applyOp with existing doc path
	if err := ps.Upsert(db, pattern, tmplID, docID, []byte{0x02}, ""); err != nil {
		t.Fatalf("Upsert 2 failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush 2 failed: %v", err)
	}

	// Verify only one entry exists with new orderKey
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if len(results) > 0 && !bytes.Equal(results[0].OrderKey, []byte{0x02}) {
		t.Errorf("expected orderKey [0x02], got %v", results[0].OrderKey)
	}
}

// TestApplyOpDeleteExistingDocFlush tests applyOp delete path when document exists in DB.
// This specifically tests lines 369-383.
func TestApplyOpDeleteExistingDocFlush(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "tmpl1"
	docID := "doc1"

	// Insert first
	if err := ps.Upsert(db, pattern, tmplID, docID, []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush 1 failed: %v", err)
	}

	// Verify it exists
	_, found := ps.Get(db, pattern, tmplID, docID)
	if !found {
		t.Fatal("doc should exist before delete")
	}

	// Delete - this triggers applyOp delete path with existing doc
	if err := ps.Delete(db, pattern, tmplID, docID, ""); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush 2 failed: %v", err)
	}

	// Verify it's gone
	_, found = ps.Get(db, pattern, tmplID, docID)
	if found {
		t.Error("doc should not exist after delete")
	}

	// Search should return empty
	results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// TestDeleteDatabase tests the DeleteDatabase function
func TestDeleteDatabase(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	db := "testdb"

	// Create some indexes for the database
	if err := ps.Upsert(db, "users/*", "tmpl1", "user1", []byte{0x01}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Upsert(db, "posts/*", "tmpl2", "post1", []byte{0x02}, ""); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := ps.Flush(); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// Verify indexes exist
	indexes, _ := ps.ListIndexes(db)
	if len(indexes) != 2 {
		t.Errorf("expected 2 indexes, got %d", len(indexes))
	}

	// Delete the database
	if err := ps.DeleteDatabase(db); err != nil {
		t.Fatalf("DeleteDatabase failed: %v", err)
	}

	// Verify indexes are deleted
	indexes, _ = ps.ListIndexes(db)
	if len(indexes) != 0 {
		t.Errorf("expected 0 indexes after delete, got %d", len(indexes))
	}
}

// TestDeleteDatabase_Empty tests deleting a database that doesn't exist
func TestDeleteDatabase_Empty(t *testing.T) {
	t.Parallel()
	ps, cleanup := setupTestStore(t)
	defer cleanup()

	// Delete a non-existent database should not error
	if err := ps.DeleteDatabase("nonexistent"); err != nil {
		t.Fatalf("DeleteDatabase failed: %v", err)
	}
}
