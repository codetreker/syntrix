package indexer_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/puller"
)

func TestIntegration_LargeDataset(t *testing.T) {
	t.Parallel()
	const docCount = 1000
	mockPullerSvc := newMockPuller(docCount + 100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()
	defer stopService(t, svc, mockPullerSvc)

	// Generate 1000 documents with random timestamps
	type docData struct {
		id        string
		timestamp int64
	}
	docs := make([]docData, docCount)
	for i := 0; i < docCount; i++ {
		docs[i] = docData{
			id:        fmt.Sprintf("user%04d", i),
			timestamp: int64(docCount - i), // Reverse order so we can verify sorting
		}
	}

	// Push all events
	for i, doc := range docs {
		evt := createTestEvent("db1", "users", doc.id, map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": doc.timestamp,
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		stats, err := svc.Stats(ctx)
		if assert.NoError(c, err) {
			assert.Equal(c, int64(docCount), stats.EventsApplied)
		}
	}, 5*time.Second, 10*time.Millisecond)

	// Search with limit
	plan := indexer.Plan{
		Collection: "users",
		OrderBy: []indexer.OrderField{
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 100,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 100 {
		t.Fatalf("expected 100 results, got %d", len(results))
	}

	// Verify first 100 results are ordered correctly (highest timestamp first)
	// user0000 has timestamp 1000, user0001 has 999, etc.
	for i, ref := range results {
		expected := fmt.Sprintf("user%04d", i)
		if ref.ID != expected {
			t.Errorf("result[%d]: expected %s, got %s", i, expected, ref.ID)
		}
	}
}

func TestIntegration_ConcurrentUpdates(t *testing.T) {
	t.Parallel()
	const docCount = 100
	const updateRounds = 5
	mockPullerSvc := newMockPuller(docCount*(updateRounds+1) + 100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert initial documents
	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i * 10),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("insert-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Update documents multiple times
	for round := 0; round < updateRounds; round++ {
		for i := 0; i < docCount; i++ {
			docID := fmt.Sprintf("user%03d", i)
			updateEvt := &puller.ChangeEvent{
				EventID:  fmt.Sprintf("evt-update-%d-%d", round, i),
				Database: "db1",
				OpType:   puller.OperationUpdate,
				FullDocument: testStoredDoc("db1", "users", docID, map[string]any{
					"id":        docID,
					"name":      fmt.Sprintf("User %d (v%d)", i, round+2),
					"timestamp": int64((round+1)*docCount + i),
				}),
				ClusterTime: puller.ClusterTime{T: uint32(time.Now().Unix()), I: uint32(round*docCount + i)},
				Timestamp:   time.Now().UnixMilli(),
			}
			mockPullerSvc.pushEvent(updateEvt, fmt.Sprintf("update-%d-%d", round, i))
		}
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Verify final order - after all updates, the order should be based on final timestamps
	plan := indexer.Plan{
		Collection: "users",
		OrderBy: []indexer.OrderField{
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: docCount,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != docCount {
		t.Fatalf("expected %d results, got %d", docCount, len(results))
	}

	// Final timestamps are: updateRounds*docCount + i for each user
	// So user099 has highest timestamp: 5*100 + 99 = 599
	// user000 has lowest timestamp: 5*100 + 0 = 500
	for i, ref := range results {
		expected := fmt.Sprintf("user%03d", docCount-1-i)
		if ref.ID != expected {
			t.Errorf("result[%d]: expected %s, got %s", i, expected, ref.ID)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_MultipleCollections(t *testing.T) {
	t.Parallel()
	const usersCount = 200
	const ordersCount = 150
	const productsCount = 100
	totalEvents := usersCount + ordersCount + productsCount
	mockPullerSvc := newMockPuller(totalEvents + 100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert users
	for i := 0; i < usersCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("user-%d", i))
	}

	// Insert orders
	for i := 0; i < ordersCount; i++ {
		evt := createTestEvent("db1", "orders", fmt.Sprintf("order%03d", i), map[string]any{
			"customer": fmt.Sprintf("user%03d", i%usersCount),
			"amount":   float64(i * 100),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("order-%d", i))
	}

	// Insert products
	for i := 0; i < productsCount; i++ {
		evt := createTestEvent("db1", "products", fmt.Sprintf("prod%03d", i), map[string]any{
			"name":  fmt.Sprintf("Product %d", i),
			"price": float64(i * 10),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("product-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Verify stats
	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if stats.EventsApplied != int64(totalEvents) {
		t.Errorf("expected %d events applied, got %d", totalEvents, stats.EventsApplied)
	}

	// Search users (ordered by timestamp desc)
	userPlan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      50,
	}
	userResults, err := svc.Search(ctx, "db1", userPlan)
	if err != nil {
		t.Fatalf("search users failed: %v", err)
	}
	if len(userResults) != 50 {
		t.Fatalf("expected 50 user results, got %d", len(userResults))
	}
	// Highest timestamp is 199, so first result should be user199
	if userResults[0].ID != "user199" {
		t.Errorf("expected user199, got %s", userResults[0].ID)
	}

	// Search orders (ordered by amount desc)
	orderPlan := indexer.Plan{
		Collection: "orders",
		OrderBy:    []indexer.OrderField{{Field: "amount", Direction: indexer.Desc}},
		Limit:      50,
	}
	orderResults, err := svc.Search(ctx, "db1", orderPlan)
	if err != nil {
		t.Fatalf("search orders failed: %v", err)
	}
	if len(orderResults) != 50 {
		t.Fatalf("expected 50 order results, got %d", len(orderResults))
	}
	// Highest amount is 14900 (149 * 100), so first result should be order149
	if orderResults[0].ID != "order149" {
		t.Errorf("expected order149, got %s", orderResults[0].ID)
	}

	// Search products (ordered by price asc)
	productPlan := indexer.Plan{
		Collection: "products",
		OrderBy:    []indexer.OrderField{{Field: "price", Direction: indexer.Asc}},
		Limit:      50,
	}
	productResults, err := svc.Search(ctx, "db1", productPlan)
	if err != nil {
		t.Fatalf("search products failed: %v", err)
	}
	if len(productResults) != 50 {
		t.Fatalf("expected 50 product results, got %d", len(productResults))
	}
	// Lowest price is 0, so first result should be prod000
	if productResults[0].ID != "prod000" {
		t.Errorf("expected prod000, got %s", productResults[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_BulkDeletes(t *testing.T) {
	t.Parallel()
	const docCount = 200
	mockPullerSvc := newMockPuller(docCount*2 + 100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert documents
	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("insert-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Verify all documents are indexed
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      docCount,
	}
	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("initial search failed: %v", err)
	}
	if len(results) != docCount {
		t.Fatalf("expected %d results before delete, got %d", docCount, len(results))
	}

	// Delete half the documents (even numbers)
	for i := 0; i < docCount; i += 2 {
		docID := fmt.Sprintf("user%03d", i)
		deleteEvt := &puller.ChangeEvent{
			EventID:  fmt.Sprintf("evt-delete-%d", i),
			Database: "db1",
			OpType:   puller.OperationUpdate,
			FullDocument: testDeletedDoc("db1", "users", docID, map[string]any{
				"id":        docID,
				"name":      fmt.Sprintf("User %d", i),
				"timestamp": int64(i),
			}),
			ClusterTime: puller.ClusterTime{T: uint32(time.Now().Unix()), I: uint32(i)},
			Timestamp:   time.Now().UnixMilli(),
		}
		mockPullerSvc.pushEvent(deleteEvt, fmt.Sprintf("delete-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Verify only odd-numbered documents remain
	results, err = svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search after delete failed: %v", err)
	}
	expectedCount := docCount / 2
	if len(results) != expectedCount {
		t.Fatalf("expected %d results after delete, got %d", expectedCount, len(results))
	}

	// All results should be odd-numbered users
	for _, ref := range results {
		var num int
		fmt.Sscanf(ref.ID, "user%d", &num)
		if num%2 == 0 {
			t.Errorf("found deleted user in results: %s", ref.ID)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_PatternWithManyUsers(t *testing.T) {
	t.Parallel()
	const userCount = 20
	const chatsPerUser = 50
	totalEvents := userCount * chatsPerUser
	mockPullerSvc := newMockPuller(totalEvents + 100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Create chats for many users
	for u := 0; u < userCount; u++ {
		for c := 0; c < chatsPerUser; c++ {
			evt := createTestEvent("db1",
				fmt.Sprintf("users/user%02d/chats", u),
				fmt.Sprintf("chat-u%02d-c%03d", u, c),
				map[string]any{
					"priority":  int64(c % 5),      // Priority 0-4
					"timestamp": int64(u*1000 + c), // Unique timestamp
				})
			mockPullerSvc.pushEvent(evt, fmt.Sprintf("chat-%d-%d", u, c))
		}
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Verify stats
	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}
	if stats.EventsApplied != int64(totalEvents) {
		t.Errorf("expected %d events applied, got %d", totalEvents, stats.EventsApplied)
	}

	plan := indexer.Plan{
		Collection: "users/user00/chats",
		OrderBy: []indexer.OrderField{
			{Field: "priority", Direction: indexer.Desc},
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 100,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != chatsPerUser {
		t.Fatalf("expected %d results in user00's collection, got %d", chatsPerUser, len(results))
	}

	// Verify ordering: priority desc, then timestamp desc
	// All priority=4 chats should come first (c=4,9,14,19,24,29,34,39,44,49 for each user)
	// Within same priority, higher timestamp comes first
	lastPriority := int64(5) // Start higher than max
	lastTimestamp := int64(999999)
	for i, ref := range results {
		// Extract priority and timestamp from chat ID
		var u, c int
		fmt.Sscanf(ref.ID, "chat-u%02d-c%03d", &u, &c)
		if u != 0 {
			t.Errorf("result[%d] belongs to another collection: %s", i, ref.ID)
		}
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

func TestIntegration_MultiDatabaseLargeScale(t *testing.T) {
	t.Parallel()
	const dbCount = 5
	const docsPerDB = 100
	totalEvents := dbCount * docsPerDB
	mockPullerSvc := newMockPuller(totalEvents + 100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
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

	waitIntegrationEvents(t, svc, mockPullerSvc)

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

		// Verify first result is user099 (highest timestamp in this db)
		if results[0].ID != "user099" {
			t.Errorf("db%d: expected user099, got %s", db, results[0].ID)
		}

		// Verify last result is user000 (lowest timestamp in this db)
		if results[docsPerDB-1].ID != "user000" {
			t.Errorf("db%d: expected user000, got %s", db, results[docsPerDB-1].ID)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_MixedOperations(t *testing.T) {
	t.Parallel()
	const initialDocs = 100
	mockPullerSvc := newMockPuller(500)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Phase 1: Insert initial documents
	for i := 0; i < initialDocs; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("insert-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Phase 2: Mixed operations - update some, delete some, insert new
	// Update users 0-19 (bump their timestamps)
	for i := 0; i < 20; i++ {
		docID := fmt.Sprintf("user%03d", i)
		updateEvt := &puller.ChangeEvent{
			EventID:  fmt.Sprintf("evt-update-%d", i),
			Database: "db1",
			OpType:   puller.OperationUpdate,
			FullDocument: testStoredDoc("db1", "users", docID, map[string]any{
				"id":        docID,
				"name":      fmt.Sprintf("User %d (Updated)", i),
				"timestamp": int64(1000 + i), // Bump to top
			}),
			ClusterTime: puller.ClusterTime{T: uint32(time.Now().Unix()), I: uint32(i)},
			Timestamp:   time.Now().UnixMilli(),
		}
		mockPullerSvc.pushEvent(updateEvt, fmt.Sprintf("update-%d", i))
	}

	// Delete users 80-99
	for i := 80; i < 100; i++ {
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

	// Insert new users 100-119
	for i := 100; i < 120; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(500 + i), // Mid-range timestamps
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("new-insert-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Verify final state
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      200,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	// Expected count: 100 - 20 (deleted) + 20 (new) = 100
	expectedCount := 100
	if len(results) != expectedCount {
		t.Fatalf("expected %d results, got %d", expectedCount, len(results))
	}

	// Verify updated users (0-19) are at the top
	// Their timestamps are 1000-1019, so they should be first 20 results
	for i := 0; i < 20; i++ {
		expected := fmt.Sprintf("user%03d", 19-i) // 1019, 1018, ..., 1000
		if results[i].ID != expected {
			t.Errorf("result[%d]: expected %s, got %s", i, expected, results[i].ID)
		}
	}

	// Verify deleted users (80-99) are not in results
	resultIDs := make(map[string]bool)
	for _, ref := range results {
		resultIDs[ref.ID] = true
	}
	for i := 80; i < 100; i++ {
		deletedID := fmt.Sprintf("user%03d", i)
		if resultIDs[deletedID] {
			t.Errorf("deleted user %s found in results", deletedID)
		}
	}

	// Verify new users (100-119) are in results
	for i := 100; i < 120; i++ {
		newID := fmt.Sprintf("user%03d", i)
		if !resultIDs[newID] {
			t.Errorf("new user %s not found in results", newID)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_EventIndexing(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Push some events
	events := []*puller.ChangeEvent{
		createTestEvent("db1", "users", "user1", map[string]any{
			"name":      "Alice",
			"timestamp": int64(1000),
		}),
		createTestEvent("db1", "users", "user2", map[string]any{
			"name":      "Bob",
			"timestamp": int64(2000),
		}),
		createTestEvent("db1", "users", "user3", map[string]any{
			"name":      "Charlie",
			"timestamp": int64(1500),
		}),
	}

	for i, evt := range events {
		mockPullerSvc.pushEvent(evt, "progress-"+string(rune('a'+i)))
	}

	// Wait for events to be processed
	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Verify stats
	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("failed to get stats: %v", err)
	}

	if stats.EventsApplied != 3 {
		t.Errorf("expected 3 events applied, got %d", stats.EventsApplied)
	}

	// Search for users ordered by timestamp desc
	plan := indexer.Plan{
		Collection: "users",
		OrderBy: []indexer.OrderField{
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 10,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Results should be ordered by timestamp desc: user2 (2000), user3 (1500), user1 (1000)
	expectedOrder := []string{"user2", "user3", "user1"}
	for i, ref := range results {
		if ref.ID != expectedOrder[i] {
			t.Errorf("result[%d]: expected %s, got %s", i, expectedOrder[i], ref.ID)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_PatternMatching(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Push events for pattern "users/*/chats"
	events := []*puller.ChangeEvent{
		createTestEvent("db1", "users/alice/chats", "chat1", map[string]any{
			"priority":  int64(1),
			"timestamp": int64(100),
		}),
		createTestEvent("db1", "users/alice/chats", "chat2", map[string]any{
			"priority":  int64(3),
			"timestamp": int64(200),
		}),
		createTestEvent("db1", "users/bob/chats", "chat3", map[string]any{
			"priority":  int64(2),
			"timestamp": int64(150),
		}),
	}

	for i, evt := range events {
		mockPullerSvc.pushEvent(evt, "p-"+string(rune('a'+i)))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	plan := indexer.Plan{
		Collection: "users/alice/chats",
		OrderBy: []indexer.OrderField{
			{Field: "priority", Direction: indexer.Desc},
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 10,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results in Alice's collection, got %d", len(results))
	}

	expectedOrder := []string{"chat2", "chat1"}
	for i, ref := range results {
		if ref.ID != expectedOrder[i] {
			t.Errorf("result[%d]: expected %s, got %s", i, expectedOrder[i], ref.ID)
		}
	}
	plan.Collection = "users/bob/chats"
	results, err = svc.Search(ctx, "db1", plan)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "chat3", results[0].ID)

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_NoMatchingIndex(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Search for a collection that has no matching template
	plan := indexer.Plan{
		Collection: "nonexistent",
		OrderBy: []indexer.OrderField{
			{Field: "createdAt", Direction: indexer.Desc},
		},
		Limit: 10,
	}

	_, err := svc.Search(ctx, "db1", plan)
	if err != indexer.ErrNoMatchingIndex {
		t.Errorf("expected ErrNoMatchingIndex, got %v", err)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_DocumentUpdate(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert a document
	mockPullerSvc.pushEvent(createTestEvent("db1", "users", "user1", map[string]any{
		"name":      "Alice",
		"timestamp": int64(1000),
	}), "p1")

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Update the document with a new timestamp
	updateEvt := &puller.ChangeEvent{
		EventID:  "evt-update1",
		Database: "db1",
		OpType:   puller.OperationUpdate,
		FullDocument: testStoredDoc("db1", "users", "user1", map[string]any{
			"id":        "user1",
			"name":      "Alice Updated",
			"timestamp": int64(3000), // Higher timestamp, should move to first position
		}),
		ClusterTime: puller.ClusterTime{T: uint32(time.Now().Unix()), I: 2},
		Timestamp:   time.Now().UnixMilli(),
	}
	mockPullerSvc.pushEvent(updateEvt, "p2")

	// Insert another document
	mockPullerSvc.pushEvent(createTestEvent("db1", "users", "user2", map[string]any{
		"name":      "Bob",
		"timestamp": int64(2000),
	}), "p3")

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search - user1 should now be first due to higher timestamp
	plan := indexer.Plan{
		Collection: "users",
		OrderBy: []indexer.OrderField{
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 10,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// After update: user1 (3000), user2 (2000)
	expectedOrder := []string{"user1", "user2"}
	for i, ref := range results {
		if ref.ID != expectedOrder[i] {
			t.Errorf("result[%d]: expected %s, got %s", i, expectedOrder[i], ref.ID)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_DocumentDelete(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert two documents
	mockPullerSvc.pushEvent(createTestEvent("db1", "users", "user1", map[string]any{
		"name":      "Alice",
		"timestamp": int64(1000),
	}), "p1")

	mockPullerSvc.pushEvent(createTestEvent("db1", "users", "user2", map[string]any{
		"name":      "Bob",
		"timestamp": int64(2000),
	}), "p2")

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Delete user2 (soft delete)
	deleteEvt := &puller.ChangeEvent{
		EventID:  "evt-delete1",
		Database: "db1",
		OpType:   puller.OperationUpdate,
		FullDocument: testDeletedDoc("db1", "users", "user2", map[string]any{
			"id":        "user2",
			"name":      "Bob",
			"timestamp": int64(2000),
		}),
		ClusterTime: puller.ClusterTime{T: uint32(time.Now().Unix()), I: 3},
		Timestamp:   time.Now().UnixMilli(),
	}
	mockPullerSvc.pushEvent(deleteEvt, "p3")

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search - only user1 should remain
	plan := indexer.Plan{
		Collection: "users",
		OrderBy: []indexer.OrderField{
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 10,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result after delete, got %d", len(results))
	}

	if results[0].ID != "user1" {
		t.Errorf("expected user1, got %s", results[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_MultipleDatabase(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert documents into two different databases
	mockPullerSvc.pushEvent(createTestEvent("db1", "users", "user1", map[string]any{
		"name":      "Alice-DB1",
		"timestamp": int64(1000),
	}), "p1")

	mockPullerSvc.pushEvent(createTestEvent("db2", "users", "user1", map[string]any{
		"name":      "Alice-DB2",
		"timestamp": int64(2000),
	}), "p2")

	mockPullerSvc.pushEvent(createTestEvent("db1", "users", "user2", map[string]any{
		"name":      "Bob-DB1",
		"timestamp": int64(3000),
	}), "p3")

	waitIntegrationEvents(t, svc, mockPullerSvc)

	plan := indexer.Plan{
		Collection: "users",
		OrderBy: []indexer.OrderField{
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 10,
	}

	// Search db1
	results1, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search db1 failed: %v", err)
	}

	if len(results1) != 2 {
		t.Fatalf("expected 2 results for db1, got %d", len(results1))
	}

	// db1: user2 (3000), user1 (1000)
	if results1[0].ID != "user2" || results1[1].ID != "user1" {
		t.Errorf("db1 results order incorrect: %v, %v", results1[0].ID, results1[1].ID)
	}

	// Search db2
	results2, err := svc.Search(ctx, "db2", plan)
	if err != nil {
		t.Fatalf("search db2 failed: %v", err)
	}

	if len(results2) != 1 {
		t.Fatalf("expected 1 result for db2, got %d", len(results2))
	}

	if results2[0].ID != "user1" {
		t.Errorf("expected user1 in db2, got %s", results2[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}
