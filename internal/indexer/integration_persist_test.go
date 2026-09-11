package indexer_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/puller"
)

func TestIntegration_Pebble_BasicOperations(t *testing.T) {
	t.Parallel()
	const docCount = 1000
	dataDir := t.TempDir()
	mockPullerSvc := newMockPuller(docCount + 100)
	svc, ctx, cancel := setupIndexerServiceWithPebble(t, mockPullerSvc, dataDir)
	defer cancel()

	// Insert documents
	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(docCount - i), // Reverse order for desc sorting
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	// Wait for events to be processed and flush to ensure all writes are complete
	waitIntegrationEvents(t, svc, mockPullerSvc)
	if err := svc.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	// Verify stats
	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if stats.EventsApplied != docCount {
		t.Errorf("expected %d events applied, got %d", docCount, stats.EventsApplied)
	}

	// Search with limit
	plan := indexer.Plan{
		Collection: "users",
		OrderBy: []indexer.OrderField{
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 50,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 50 {
		t.Fatalf("expected 50 results, got %d", len(results))
	}

	// Verify first 50 results are ordered correctly (highest timestamp first)
	for i, ref := range results {
		expected := fmt.Sprintf("user%03d", i)
		if ref.ID != expected {
			t.Errorf("result[%d]: expected %s, got %s", i, expected, ref.ID)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_Pebble_Persistence(t *testing.T) {
	t.Parallel()
	const docCount = 500
	dataDir := t.TempDir()

	// Phase 1: Create service, insert data, stop
	mockPullerSvc1 := newMockPuller(docCount + 100)
	svc1, ctx1, cancel1 := setupIndexerServiceWithPebble(t, mockPullerSvc1, dataDir)

	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc1.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	// Wait for events and flush
	waitIntegrationEvents(t, svc1, mockPullerSvc1)
	if err := svc1.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	// Verify data before stop
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      docCount,
	}

	results1, err := svc1.Search(ctx1, "db1", plan)
	if err != nil {
		t.Fatalf("search before stop failed: %v", err)
	}
	if len(results1) != docCount {
		t.Fatalf("expected %d results before stop, got %d", docCount, len(results1))
	}

	// Stop the first service
	stopService(t, svc1, mockPullerSvc1)
	cancel1()

	// Phase 2: Restart with same data dir and verify data persisted
	mockPullerSvc2 := newMockPuller(100)
	svc2, ctx2, cancel2 := setupIndexerServiceWithPebble(t, mockPullerSvc2, dataDir)
	defer cancel2()

	// Wait for service to fully initialize
	waitIntegrationEvents(t, svc2, mockPullerSvc2)

	// Search again - data should be persisted
	results2, err := svc2.Search(ctx2, "db1", plan)
	if err != nil {
		t.Fatalf("search after restart failed: %v", err)
	}
	if len(results2) != docCount {
		t.Fatalf("expected %d results after restart, got %d", docCount, len(results2))
	}

	// Verify order is correct
	if results2[0].ID != "user499" {
		t.Errorf("expected user499 first after restart, got %s", results2[0].ID)
	}
	if results2[docCount-1].ID != "user000" {
		t.Errorf("expected user000 last after restart, got %s", results2[docCount-1].ID)
	}

	stopService(t, svc2, mockPullerSvc2)
}

func TestIntegration_Pebble_ProgressPersistence(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	// Phase 1: Create service, insert data with progress markers
	mockPullerSvc1 := newMockPuller(500)
	svc1, _, cancel1 := setupIndexerServiceWithPebble(t, mockPullerSvc1, dataDir)

	for i := 0; i < 200; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"timestamp": int64(i),
		})
		mockPullerSvc1.pushEvent(evt, fmt.Sprintf("progress-checkpoint-%03d", i))
	}

	// Wait for events and flush
	waitIntegrationEvents(t, svc1, mockPullerSvc1)
	if err := svc1.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	// Stop first service
	stopService(t, svc1, mockPullerSvc1)
	cancel1()

	// Phase 2: Restart and verify progress was persisted
	mockPullerSvc2 := newMockPuller(500)
	svc2, _, cancel2 := setupIndexerServiceWithPebble(t, mockPullerSvc2, dataDir)
	defer cancel2()

	waitIntegrationEvents(t, svc2, mockPullerSvc2)

	// Get manager and check progress
	mgr := svc2.Manager()
	progress, err := mgr.LoadProgress()
	if err != nil {
		t.Fatalf("failed to load progress: %v", err)
	}

	// Progress should be the last checkpoint
	if progress != "progress-checkpoint-199" {
		t.Errorf("expected progress 'progress-checkpoint-199', got %s", progress)
	}

	stopService(t, svc2, mockPullerSvc2)
}

func TestIntegration_Pebble_UpdatesAndDeletes(t *testing.T) {
	t.Parallel()
	const docCount = 500
	dataDir := t.TempDir()
	mockPullerSvc := newMockPuller(docCount*3 + 100)
	svc, ctx, cancel := setupIndexerServiceWithPebble(t, mockPullerSvc, dataDir)
	defer cancel()

	// Insert documents
	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("insert-%d", i))
	}

	// Wait for inserts and flush
	waitIntegrationEvents(t, svc, mockPullerSvc)
	if err := svc.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush inserts: %v", err)
	}

	// Update first 10 users with higher timestamps
	for i := 0; i < 10; i++ {
		docID := fmt.Sprintf("user%03d", i)
		updateEvt := &puller.ChangeEvent{
			EventID:  fmt.Sprintf("evt-update-%d", i),
			Database: "db1",
			OpType:   puller.OperationUpdate,
			FullDocument: testStoredDoc("db1", "users", docID, map[string]any{
				"id":        docID,
				"name":      fmt.Sprintf("User %d Updated", i),
				"timestamp": int64(1000 + i),
			}),
			ClusterTime: puller.ClusterTime{T: uint32(time.Now().Unix()), I: uint32(i)},
			Timestamp:   time.Now().UnixMilli(),
		}
		mockPullerSvc.pushEvent(updateEvt, fmt.Sprintf("update-%d", i))
	}

	// Delete users 40-49
	for i := 40; i < 50; i++ {
		docID := fmt.Sprintf("user%03d", i)
		deleteEvt := &puller.ChangeEvent{
			EventID:      fmt.Sprintf("evt-delete-%d", i),
			Database:     "db1",
			OpType:       puller.OperationUpdate,
			FullDocument: testDeletedDoc("db1", "users", docID, map[string]any{"id": docID, "timestamp": int64(i)}),
			ClusterTime:  puller.ClusterTime{T: uint32(time.Now().Unix()), I: uint32(100 + i)},
			Timestamp:    time.Now().UnixMilli(),
		}
		mockPullerSvc.pushEvent(deleteEvt, fmt.Sprintf("delete-%d", i))
	}

	// Wait for updates/deletes and flush
	waitIntegrationEvents(t, svc, mockPullerSvc)
	if err := svc.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush updates/deletes: %v", err)
	}

	// Verify final state
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      docCount,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	// Expected: 500 - 10 (deleted) = 490 users
	expectedCount := docCount - 10
	if len(results) != expectedCount {
		t.Fatalf("expected %d results, got %d", expectedCount, len(results))
	}

	// Updated users (0-9) should be first due to higher timestamps
	for i := 0; i < 10; i++ {
		expected := fmt.Sprintf("user%03d", 9-i) // 1009, 1008, ..., 1000
		if results[i].ID != expected {
			t.Errorf("result[%d]: expected %s, got %s", i, expected, results[i].ID)
		}
	}

	// Verify deleted users (40-49) are not in results
	resultIDs := make(map[string]bool)
	for _, ref := range results {
		resultIDs[ref.ID] = true
	}
	for i := 40; i < 50; i++ {
		deletedID := fmt.Sprintf("user%03d", i)
		if resultIDs[deletedID] {
			t.Errorf("deleted user %s found in results", deletedID)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_Pebble_MultiDatabase(t *testing.T) {
	t.Parallel()
	const dbCount = 5
	const docsPerDB = 200
	totalEvents := dbCount * docsPerDB
	dataDir := t.TempDir()
	mockPullerSvc := newMockPuller(totalEvents + 100)
	svc, ctx, cancel := setupIndexerServiceWithPebble(t, mockPullerSvc, dataDir)
	defer cancel()

	// Insert documents across multiple databases
	for db := 0; db < dbCount; db++ {
		for i := 0; i < docsPerDB; i++ {
			evt := createTestEvent(
				fmt.Sprintf("db%d", db),
				"users",
				fmt.Sprintf("user%03d", i),
				map[string]any{
					"name":      fmt.Sprintf("User %d in DB%d", i, db),
					"timestamp": int64(db*1000 + i),
				})
			mockPullerSvc.pushEvent(evt, fmt.Sprintf("db%d-user%d", db, i))
		}
	}

	// Wait for events and flush
	waitIntegrationEvents(t, svc, mockPullerSvc)
	if err := svc.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	// Verify stats
	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if stats.EventsApplied != int64(totalEvents) {
		t.Errorf("expected %d events applied, got %d", totalEvents, stats.EventsApplied)
	}

	// Search each database
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      docsPerDB,
	}

	for db := 0; db < dbCount; db++ {
		results, err := svc.Search(ctx, fmt.Sprintf("db%d", db), plan)
		if err != nil {
			t.Fatalf("search db%d failed: %v", db, err)
		}
		if len(results) != docsPerDB {
			t.Fatalf("db%d: expected %d results, got %d", db, docsPerDB, len(results))
		}

		// First result should be user199 (highest timestamp in this db)
		if results[0].ID != "user199" {
			t.Errorf("db%d: expected user199, got %s", db, results[0].ID)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_Pebble_LargeDataset(t *testing.T) {
	t.Parallel()
	const docCount = 2000
	dataDir := t.TempDir()
	mockPullerSvc := newMockPuller(docCount + 100)
	svc, ctx, cancel := setupIndexerServiceWithPebble(t, mockPullerSvc, dataDir)
	defer cancel()

	// Insert many documents
	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%05d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	// Wait for processing and flush
	waitIntegrationEvents(t, svc, mockPullerSvc)
	if err := svc.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	// Verify all events processed
	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("stats failed: %v", err)
	}
	if stats.EventsApplied != docCount {
		t.Errorf("expected %d events, got %d", docCount, stats.EventsApplied)
	}

	// Verify search works correctly
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      100,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 100 {
		t.Fatalf("expected 100 results, got %d", len(results))
	}

	// First result should be user01999 (highest timestamp)
	if results[0].ID != "user01999" {
		t.Errorf("expected user01999, got %s", results[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_Pebble_PatternMatching(t *testing.T) {
	t.Parallel()
	const userCount = 10
	const chatsPerUser = 100
	totalEvents := userCount * chatsPerUser
	dataDir := t.TempDir()
	mockPullerSvc := newMockPuller(totalEvents + 100)
	svc, ctx, cancel := setupIndexerServiceWithPebble(t, mockPullerSvc, dataDir)
	defer cancel()

	// Create chats for multiple users
	for u := 0; u < userCount; u++ {
		for c := 0; c < chatsPerUser; c++ {
			evt := createTestEvent("db1",
				fmt.Sprintf("users/user%02d/chats", u),
				fmt.Sprintf("chat-u%02d-c%03d", u, c),
				map[string]any{
					"priority":  int64(c % 5),
					"timestamp": int64(u*1000 + c),
				})
			mockPullerSvc.pushEvent(evt, fmt.Sprintf("chat-%d-%d", u, c))
		}
	}

	// Wait for events and flush
	waitIntegrationEvents(t, svc, mockPullerSvc)
	if err := svc.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	// Search - all chats from all users are in the same index
	plan := indexer.Plan{
		Collection: "users/user00/chats",
		OrderBy: []indexer.OrderField{
			{Field: "priority", Direction: indexer.Desc},
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 50,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 50 {
		t.Fatalf("expected 50 results, got %d", len(results))
	}

	// Verify ordering: priority desc, then timestamp desc
	lastPriority := int64(5)
	lastTimestamp := int64(999999)
	for i, ref := range results {
		var u, c int
		fmt.Sscanf(ref.ID, "chat-u%02d-c%03d", &u, &c)
		priority := int64(c % 5)
		timestamp := int64(u*1000 + c)

		if priority > lastPriority {
			t.Errorf("result[%d]: priority %d > previous %d", i, priority, lastPriority)
		} else if priority == lastPriority && timestamp > lastTimestamp {
			t.Errorf("result[%d]: timestamp %d > previous %d with same priority", i, timestamp, lastTimestamp)
		}
		lastPriority = priority
		lastTimestamp = timestamp
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_Pebble_FlushOnStop(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	// Phase 1: Create, insert data, and stop
	mockPullerSvc1 := newMockPuller(500)
	svc1, _, cancel1 := setupIndexerServiceWithPebble(t, mockPullerSvc1, dataDir)

	// Insert 300 documents
	for i := 0; i < 300; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"timestamp": int64(i),
		})
		mockPullerSvc1.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	// Wait for events to be processed (read from channel)
	waitIntegrationEvents(t, svc1, mockPullerSvc1)

	// Stop should flush all pending data
	stopService(t, svc1, mockPullerSvc1)
	cancel1()

	// Phase 2: Restart and verify all data was persisted via Flush
	mockPullerSvc2 := newMockPuller(500)
	svc2, ctx2, cancel2 := setupIndexerServiceWithPebble(t, mockPullerSvc2, dataDir)
	defer cancel2()

	waitIntegrationEvents(t, svc2, mockPullerSvc2)

	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      500,
	}

	results, err := svc2.Search(ctx2, "db1", plan)
	if err != nil {
		t.Fatalf("search after restart failed: %v", err)
	}

	// All 300 documents should be persisted
	if len(results) != 300 {
		t.Fatalf("expected 300 results after restart with flush, got %d", len(results))
	}

	stopService(t, svc2, mockPullerSvc2)
}

func TestIntegration_Pebble_PaginationPersistence(t *testing.T) {
	t.Parallel()
	const docCount = 1000
	dataDir := t.TempDir()

	// Phase 1: Create service, insert data, paginate, then stop
	mockPullerSvc1 := newMockPuller(docCount + 100)
	svc1, ctx1, cancel1 := setupIndexerServiceWithPebble(t, mockPullerSvc1, dataDir)

	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(docCount - i), // Reverse order for desc sorting
		})
		mockPullerSvc1.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	// Wait for events and flush
	waitIntegrationEvents(t, svc1, mockPullerSvc1)
	if err := svc1.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	// Fetch first page
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      20,
	}

	page1, err := svc1.Search(ctx1, "db1", plan)
	if err != nil {
		t.Fatalf("page 1 search failed: %v", err)
	}
	if len(page1) != 20 {
		t.Fatalf("expected 20 results in page 1, got %d", len(page1))
	}

	// Get cursor from last result of page 1
	cursor := encoding.EncodeBase64(page1[len(page1)-1].OrderKey)

	// Stop the first service
	stopService(t, svc1, mockPullerSvc1)
	cancel1()

	// Phase 2: Restart and continue pagination using the cursor
	mockPullerSvc2 := newMockPuller(100)
	svc2, ctx2, cancel2 := setupIndexerServiceWithPebble(t, mockPullerSvc2, dataDir)
	defer cancel2()

	waitIntegrationEvents(t, svc2, mockPullerSvc2)

	// Fetch page 2 using cursor from page 1
	plan.StartAfter = cursor
	page2, err := svc2.Search(ctx2, "db1", plan)
	if err != nil {
		t.Fatalf("page 2 search after restart failed: %v", err)
	}
	if len(page2) != 20 {
		t.Fatalf("expected 20 results in page 2, got %d", len(page2))
	}

	// Verify page 2 continues from where page 1 ended
	// Page 1 ended at user019, page 2 should start at user020
	if page2[0].ID != "user020" {
		t.Errorf("expected user020 first in page 2, got %s", page2[0].ID)
	}
	if page2[19].ID != "user039" {
		t.Errorf("expected user039 last in page 2, got %s", page2[19].ID)
	}

	// Continue to page 3 to verify ongoing pagination works
	cursor2 := encoding.EncodeBase64(page2[len(page2)-1].OrderKey)
	plan.StartAfter = cursor2
	page3, err := svc2.Search(ctx2, "db1", plan)
	if err != nil {
		t.Fatalf("page 3 search failed: %v", err)
	}
	if len(page3) != 20 {
		t.Fatalf("expected 20 results in page 3, got %d", len(page3))
	}
	if page3[0].ID != "user040" {
		t.Errorf("expected user040 first in page 3, got %s", page3[0].ID)
	}

	stopService(t, svc2, mockPullerSvc2)
}

func TestIntegration_Pebble_MultiTemplatePersistence(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	// Phase 1: Rebuild with an additional template, ingest more data, and stop.
	mockPullerSvc1 := newMockPuller(1000)
	svc1, ctx1, cancel1 := setupIndexerServiceWithPebble(t, mockPullerSvc1, dataDir)

	// Insert users with timestamp
	for i := 0; i < 300; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
			"score":     int64(i * 10),
		})
		mockPullerSvc1.pushEvent(evt, fmt.Sprintf("user-%d", i))
	}

	waitIntegrationEvents(t, svc1, mockPullerSvc1)
	if err := svc1.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	// Rebuild with a new template for score-based sorting.
	newTemplateYAML := `
templates:
  - name: users_by_timestamp
    collectionPattern: "users"
    fields:
      - field: timestamp
        order: desc

  - name: users_by_score
    collectionPattern: "users"
    fields:
      - field: score
        order: desc

  - name: chats_by_priority
    collectionPattern: "users/{userId}/chats"
    fields:
      - field: priority
        order: desc
      - field: timestamp
        order: desc

  - name: orders_by_amount
    collectionPattern: "orders"
    fields:
      - field: amount
        order: desc

  - name: products_by_price
    collectionPattern: "products"
    fields:
      - field: price
        order: asc
`

	svc1, ctx1, cancel1 = reloadIntegrationTemplates(t, svc1, mockPullerSvc1, newTemplateYAML)

	// Insert more users - these will be indexed by both templates
	for i := 300; i < 500; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
			"score":     int64(i * 10),
		})
		mockPullerSvc1.pushEvent(evt, fmt.Sprintf("user-%d", i))
	}

	waitIntegrationEvents(t, svc1, mockPullerSvc1)
	if err := svc1.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush after new template: %v", err)
	}

	// Verify score-based search works
	scorePlan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "score", Direction: indexer.Desc}},
		Limit:      100,
	}

	scoreResults1, err := svc1.Search(ctx1, "db1", scorePlan)
	if err != nil {
		t.Fatalf("score search before restart failed: %v", err)
	}
	if len(scoreResults1) != 100 {
		t.Fatalf("expected 100 score results before restart, got %d", len(scoreResults1))
	}
	// user499 has highest score (4990)
	if scoreResults1[0].ID != "user499" {
		t.Errorf("expected user499 first by score before restart, got %s", scoreResults1[0].ID)
	}

	// Stop the first service
	stopService(t, svc1, mockPullerSvc1)
	cancel1()

	// Phase 2: Restart with same templates and verify both indexes persisted
	mockPullerSvc2 := newMockPuller(500)
	svc2, ctx2, cancel2 := setupIntegrationService(t, mockPullerSvc2, config.Config{StorageMode: config.StorageModePebble, Store: config.StoreConfig{Path: dataDir, BatchSize: 10, BatchInterval: 10 * time.Millisecond}}, newTemplateYAML)
	defer cancel2()

	waitIntegrationEvents(t, svc2, mockPullerSvc2)

	// Verify timestamp-based search still works
	timestampPlan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      500,
	}

	timestampResults, err := svc2.Search(ctx2, "db1", timestampPlan)
	if err != nil {
		t.Fatalf("timestamp search after restart failed: %v", err)
	}
	if len(timestampResults) != 500 {
		t.Fatalf("expected 500 timestamp results after restart, got %d", len(timestampResults))
	}
	if timestampResults[0].ID != "user499" {
		t.Errorf("expected user499 first by timestamp after restart, got %s", timestampResults[0].ID)
	}

	// The complete rebuilt score index survives restart.
	scorePlan.Limit = 500
	scoreResults2, err := svc2.Search(ctx2, "db1", scorePlan)
	if err != nil {
		t.Fatalf("score search after restart failed: %v", err)
	}
	if len(scoreResults2) != 500 {
		t.Fatalf("expected 500 score results after restart, got %d", len(scoreResults2))
	}
	if scoreResults2[499].ID != "user000" {
		t.Errorf("expected pre-rebuild user000 last by score, got %s", scoreResults2[499].ID)
	}
	if scoreResults2[0].ID != "user499" {
		t.Errorf("expected user499 first by score after restart, got %s", scoreResults2[0].ID)
	}

	stopService(t, svc2, mockPullerSvc2)
}

func TestIntegration_Pebble_ContinueProcessingAfterRestart(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	// Phase 1: Create service, insert initial data, stop
	mockPullerSvc1 := newMockPuller(1000)
	svc1, ctx1, cancel1 := setupIndexerServiceWithPebble(t, mockPullerSvc1, dataDir)

	// Insert first batch of users (0-299)
	for i := 0; i < 300; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc1.pushEvent(evt, fmt.Sprintf("progress-%03d", i))
	}

	waitIntegrationEvents(t, svc1, mockPullerSvc1)
	if err := svc1.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	// Verify initial data
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      1000,
	}

	results1, err := svc1.Search(ctx1, "db1", plan)
	if err != nil {
		t.Fatalf("search before restart failed: %v", err)
	}
	if len(results1) != 300 {
		t.Fatalf("expected 300 results before restart, got %d", len(results1))
	}

	// Check progress was saved
	mgr1 := svc1.Manager()
	progress1, err := mgr1.LoadProgress()
	if err != nil {
		t.Fatalf("failed to load progress: %v", err)
	}
	if progress1 != "progress-299" {
		t.Errorf("expected progress 'progress-299', got %s", progress1)
	}

	// Stop the first service
	stopService(t, svc1, mockPullerSvc1)
	cancel1()

	// Phase 2: Restart and continue processing new events
	mockPullerSvc2 := newMockPuller(1000)
	svc2, ctx2, cancel2 := setupIndexerServiceWithPebble(t, mockPullerSvc2, dataDir)
	defer cancel2()

	waitIntegrationEvents(t, svc2, mockPullerSvc2)

	// Verify old data is still there
	results2, err := svc2.Search(ctx2, "db1", plan)
	if err != nil {
		t.Fatalf("search after restart failed: %v", err)
	}
	if len(results2) != 300 {
		t.Fatalf("expected 300 results after restart (before new events), got %d", len(results2))
	}

	// Verify progress was restored
	mgr2 := svc2.Manager()
	progress2, err := mgr2.LoadProgress()
	if err != nil {
		t.Fatalf("failed to load progress after restart: %v", err)
	}
	if progress2 != "progress-299" {
		t.Errorf("expected restored progress 'progress-299', got %s", progress2)
	}

	// Insert second batch of users (300-599)
	for i := 300; i < 600; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc2.pushEvent(evt, fmt.Sprintf("progress-%03d", i))
	}

	waitIntegrationEvents(t, svc2, mockPullerSvc2)
	if err := svc2.Manager().Flush(); err != nil {
		t.Fatalf("failed to flush new events: %v", err)
	}

	// Verify combined data (old + new)
	results3, err := svc2.Search(ctx2, "db1", plan)
	if err != nil {
		t.Fatalf("search after new events failed: %v", err)
	}
	if len(results3) != 600 {
		t.Fatalf("expected 600 results after new events, got %d", len(results3))
	}

	// Verify ordering - user599 should be first (highest timestamp)
	if results3[0].ID != "user599" {
		t.Errorf("expected user599 first after new events, got %s", results3[0].ID)
	}
	if results3[599].ID != "user000" {
		t.Errorf("expected user000 last after new events, got %s", results3[599].ID)
	}

	// Verify progress was updated
	progress3, err := mgr2.LoadProgress()
	if err != nil {
		t.Fatalf("failed to load final progress: %v", err)
	}
	if progress3 != "progress-599" {
		t.Errorf("expected final progress 'progress-599', got %s", progress3)
	}

	// Stop and restart again to verify all data persisted
	stopService(t, svc2, mockPullerSvc2)
	cancel2()

	// Phase 3: Final restart to verify everything persisted
	mockPullerSvc3 := newMockPuller(1000)
	svc3, ctx3, cancel3 := setupIndexerServiceWithPebble(t, mockPullerSvc3, dataDir)
	defer cancel3()

	waitIntegrationEvents(t, svc3, mockPullerSvc3)

	results4, err := svc3.Search(ctx3, "db1", plan)
	if err != nil {
		t.Fatalf("search after final restart failed: %v", err)
	}
	if len(results4) != 600 {
		t.Fatalf("expected 600 results after final restart, got %d", len(results4))
	}

	mgr3 := svc3.Manager()
	progress4, err := mgr3.LoadProgress()
	if err != nil {
		t.Fatalf("failed to load progress after final restart: %v", err)
	}
	if progress4 != "progress-599" {
		t.Errorf("expected progress 'progress-599' after final restart, got %s", progress4)
	}

	stopService(t, svc3, mockPullerSvc3)
}
