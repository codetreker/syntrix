package persist_store

import (
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/indexer/config"
	"github.com/codetreker/syntrix/internal/indexer/store"
)

// setupBenchStore creates a PebbleStore for benchmarks with configurable settings.
func setupBenchStore(b *testing.B, batchSize int, batchInterval time.Duration) (*PebbleStore, func()) {
	b.Helper()

	tmpDir, err := os.MkdirTemp("", "pebble-bench-*")
	if err != nil {
		b.Fatalf("failed to create temp dir: %v", err)
	}

	cfg := config.StoreConfig{
		Path:           filepath.Join(tmpDir, "bench.db"),
		BatchSize:      batchSize,
		BatchInterval:  batchInterval,
		QueueSize:      100000,
		BlockCacheSize: 64 * 1024 * 1024, // 64MB for benchmarks
	}

	ps, err := NewPebbleStore(cfg, slog.Default())
	if err != nil {
		os.RemoveAll(tmpDir)
		b.Fatalf("failed to create store: %v", err)
	}

	cleanup := func() {
		ps.Close()
		os.RemoveAll(tmpDir)
	}

	return ps, cleanup
}

// generateOrderKey generates an 8-byte orderKey from an integer.
func generateOrderKey(i int) []byte {
	key := make([]byte, 8)
	for j := 7; j >= 0; j-- {
		key[j] = byte(i & 0xFF)
		i >>= 8
	}
	return key
}

// ============ Upsert Benchmarks ============

func BenchmarkPebbleStore_Upsert(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 100*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
}

func BenchmarkPebbleStore_Upsert_Prealloc(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 100*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-allocate docIDs and orderKeys
	docIDs := make([]string, b.N)
	orderKeys := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		docIDs[i] = fmt.Sprintf("doc-%d", i)
		orderKeys[i] = generateOrderKey(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ps.Upsert(db, pattern, tmplID, docIDs[i], orderKeys[i], "")
	}
}

func BenchmarkPebbleStore_Upsert_BatchSizes(b *testing.B) {
	batchSizes := []int{10, 50, 100, 500, 1000}

	for _, batchSize := range batchSizes {
		b.Run(fmt.Sprintf("BatchSize-%d", batchSize), func(b *testing.B) {
			ps, cleanup := setupBenchStore(b, batchSize, 100*time.Millisecond)
			defer cleanup()

			db := "testdb"
			pattern := "users/*"
			tmplID := "created_at"

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				docID := fmt.Sprintf("doc-%d", i)
				orderKey := generateOrderKey(i)
				ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
			}
		})
	}
}

func BenchmarkPebbleStore_Upsert_Update(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert documents
	numDocs := 1000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		docID := fmt.Sprintf("doc-%d", i%numDocs)
		orderKey := generateOrderKey(i + numDocs)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
}

// ============ Get Benchmarks ============

func BenchmarkPebbleStore_Get(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert documents
	numDocs := 10000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		docID := fmt.Sprintf("doc-%d", i%numDocs)
		_, found := ps.Get(db, pattern, tmplID, docID)
		if !found {
			b.Fatalf("Get should find document %s", docID)
		}
	}
}

func BenchmarkPebbleStore_GetFromPending(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 100000, 10*time.Second) // Large batch to keep in pending
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Insert into pending
	numDocs := 10000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		docID := fmt.Sprintf("doc-%d", i%numDocs)
		_, found := ps.Get(db, pattern, tmplID, docID)
		if !found {
			b.Fatalf("Get should find document in pending %s", docID)
		}
	}
}

func BenchmarkPebbleStore_GetMiss(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert some documents
	for i := 0; i < 1000; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		docID := fmt.Sprintf("nonexistent-%d", i)
		ps.Get(db, pattern, tmplID, docID)
	}
}

// ============ Search Benchmarks ============

func BenchmarkPebbleStore_Search(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert documents
	numDocs := 10000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 100})
		if err != nil {
			b.Fatalf("Search failed: %v", err)
		}
		if len(results) != 100 {
			b.Fatalf("Expected 100 results, got %d", len(results))
		}
	}
}

func BenchmarkPebbleStore_Search_Limits(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert documents
	numDocs := 10000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	limits := []int{10, 50, 100, 500, 1000}

	for _, limit := range limits {
		b.Run(fmt.Sprintf("Limit-%d", limit), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: limit})
				if err != nil {
					b.Fatalf("Search failed: %v", err)
				}
			}
		})
	}
}

func BenchmarkPebbleStore_SearchWithBounds(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert documents
	numDocs := 10000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	lower := generateOrderKey(2000)
	upper := generateOrderKey(5000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{
			Lower: lower,
			Upper: upper,
			Limit: 100,
		})
		if err != nil {
			b.Fatalf("Search failed: %v", err)
		}
		if len(results) != 100 {
			b.Fatalf("Expected 100 results, got %d", len(results))
		}
	}
}

func BenchmarkPebbleStore_SearchWithPagination(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert documents
	numDocs := 10000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	// Simulate paginated queries
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Rotate through different start positions
		startAfter := generateOrderKey((i * 100) % numDocs)
		_, err := ps.Search(db, pattern, tmplID, store.SearchOptions{
			StartAfter: startAfter,
			Limit:      100,
		})
		if err != nil {
			b.Fatalf("Search failed: %v", err)
		}
	}
}

// BenchmarkPebbleStore_SearchWithPending benchmarks searching when a single index has
// many pending operations (5000 docs). This is an EXTREME scenario that rarely occurs
// in practice because:
//  1. Pending operations are flushed every 100ms (default BatchInterval)
//  2. Pending operations are flushed when reaching 100 ops (default BatchSize)
//
// The ~2.4ms latency comes from:
//   - Iterating 5000 pending ops to build pendingDocs slice: O(n)
//   - Sorting 5000 pending docs by orderKey: O(n log n)
//   - Memory allocations: ~700KB, ~5000 allocs
//
// In realistic scenarios with <100 pending ops per index, this path is much faster.
// See BenchmarkPebbleStore_SearchWithPendingManyIndexes for the O(1) index lookup
// optimization which helps when pending ops are spread across many indexes.
func BenchmarkPebbleStore_SearchWithPending(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 100000, 10*time.Second) // Keep pending
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert documents to DB
	numDocs := 5000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i * 2) // Even numbers
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	// Insert more to pending
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("pending-doc-%d", i)
		orderKey := generateOrderKey(i*2 + 1) // Odd numbers
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 100})
		if err != nil {
			b.Fatalf("Search failed: %v", err)
		}
		if len(results) != 100 {
			b.Fatalf("Expected 100 results, got %d", len(results))
		}
	}
}

// ============ Delete Benchmarks ============

func BenchmarkPebbleStore_Delete(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert a fixed number of documents
	numDocs := 5000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		docID := fmt.Sprintf("doc-%d", i%numDocs)
		ps.Delete(db, pattern, tmplID, docID, "")
	}
}

// ============ Concurrent Benchmarks ============

func BenchmarkPebbleStore_ConcurrentReads(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert documents
	numDocs := 10000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			docID := fmt.Sprintf("doc-%d", i%numDocs)
			ps.Get(db, pattern, tmplID, docID)
			i++
		}
	})
}

func BenchmarkPebbleStore_ConcurrentWrites(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 100*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	var counter int64
	var mu sync.Mutex

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			mu.Lock()
			i := counter
			counter++
			mu.Unlock()

			docID := fmt.Sprintf("doc-%d", i)
			orderKey := generateOrderKey(int(i))
			ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
		}
	})
}

func BenchmarkPebbleStore_ConcurrentSearches(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert documents
	numDocs := 10000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 100})
		}
	})
}

func BenchmarkPebbleStore_MixedReadWrite(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 100*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Pre-insert documents
	numDocs := 10000
	for i := 0; i < numDocs; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	var writeCounter int64
	var mu sync.Mutex
	rng := rand.New(rand.NewSource(42))

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		localRng := rand.New(rand.NewSource(rng.Int63()))
		for pb.Next() {
			if localRng.Float32() < 0.2 { // 20% writes
				mu.Lock()
				i := writeCounter
				writeCounter++
				mu.Unlock()

				docID := fmt.Sprintf("new-doc-%d", i)
				orderKey := generateOrderKey(int(i) + numDocs)
				ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
			} else { // 80% reads
				docID := fmt.Sprintf("doc-%d", localRng.Intn(numDocs))
				ps.Get(db, pattern, tmplID, docID)
			}
		}
	})
}

// ============ Memory and Large Data Benchmarks ============

func BenchmarkPebbleStore_LargeOrderKey(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	// Create large orderKey (1KB)
	largeOrderKey := make([]byte, 1024)
	for i := range largeOrderKey {
		largeOrderKey[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		docID := fmt.Sprintf("doc-%d", i)
		ps.Upsert(db, pattern, tmplID, docID, largeOrderKey, "")
	}
}

func BenchmarkPebbleStore_ManyIndexes(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	numIndexes := 100

	// Create many indexes
	for i := 0; i < numIndexes; i++ {
		pattern := fmt.Sprintf("collection%d/*", i)
		tmplID := fmt.Sprintf("tmpl%d", i)
		for j := 0; j < 100; j++ {
			docID := fmt.Sprintf("doc-%d", j)
			orderKey := generateOrderKey(j)
			ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
		}
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pattern := fmt.Sprintf("collection%d/*", i%numIndexes)
		tmplID := fmt.Sprintf("tmpl%d", i%numIndexes)
		_, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 50})
		if err != nil {
			b.Fatalf("Search failed: %v", err)
		}
	}
}

func BenchmarkPebbleStore_ListIndexes(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	db := "testdb"
	numIndexes := 50

	// Create many indexes
	for i := 0; i < numIndexes; i++ {
		pattern := fmt.Sprintf("collection%d/*", i)
		tmplID := fmt.Sprintf("tmpl%d", i)
		for j := 0; j < 10; j++ {
			docID := fmt.Sprintf("doc-%d", j)
			orderKey := generateOrderKey(j)
			ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
		}
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		indexes, _ := ps.ListIndexes(db)
		if len(indexes) != numIndexes {
			b.Fatalf("Expected %d indexes, got %d", numIndexes, len(indexes))
		}
	}
}

func BenchmarkPebbleStore_ListDatabases(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 1000, 50*time.Millisecond)
	defer cleanup()

	numDBs := 20

	// Create many databases
	for i := 0; i < numDBs; i++ {
		db := fmt.Sprintf("database%d", i)
		pattern := "users/*"
		tmplID := "created_at"
		docID := "doc1"
		orderKey := generateOrderKey(i)
		ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
	}
	if err := ps.Flush(); err != nil {
		b.Fatalf("Flush failed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dbs, _ := ps.ListDatabases()
		if len(dbs) != numDBs {
			b.Fatalf("Expected %d databases, got %d", numDBs, len(dbs))
		}
	}
}

// BenchmarkPebbleStore_SearchWithPendingManyIndexes demonstrates the O(1) index lookup
// optimization. With many indexes having pending operations, searching for a specific
// index now takes O(pending_for_that_index) instead of O(total_pending_across_all_indexes).
func BenchmarkPebbleStore_SearchWithPendingManyIndexes(b *testing.B) {
	ps, cleanup := setupBenchStore(b, 100000, 10*time.Second) // Keep pending
	defer cleanup()

	db := "testdb"
	numIndexes := 100
	docsPerIndex := 100

	// Create many indexes with pending operations
	for i := 0; i < numIndexes; i++ {
		pattern := fmt.Sprintf("collection%d/*", i)
		tmplID := fmt.Sprintf("tmpl%d", i)
		for j := 0; j < docsPerIndex; j++ {
			docID := fmt.Sprintf("doc-%d", j)
			orderKey := generateOrderKey(j)
			ps.Upsert(db, pattern, tmplID, docID, orderKey, "")
		}
	}
	// Total pending: 100 indexes * 100 docs = 10,000 pending ops

	// Search only the first index
	pattern := "collection0/*"
	tmplID := "tmpl0"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := ps.Search(db, pattern, tmplID, store.SearchOptions{Limit: 50})
		if err != nil {
			b.Fatalf("Search failed: %v", err)
		}
		if len(results) != 50 {
			b.Fatalf("Expected 50 results, got %d", len(results))
		}
	}
}
