package indexer_test

import (
	"encoding/base64"
	"fmt"
	"sort"
	"testing"

	"github.com/syntrixbase/syntrix/internal/indexer"
)

func TestIntegration_Pagination(t *testing.T) {
	t.Parallel()
	const docCount = 50
	mockPullerSvc := newMockPuller(docCount + 100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert documents
	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(docCount - i), // Reverse order for desc sorting
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Fetch first page
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      10,
	}

	page1, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("page 1 failed: %v", err)
	}
	if len(page1) != 10 {
		t.Fatalf("expected 10 results in page 1, got %d", len(page1))
	}

	// Collect all results using pagination
	allResults := make([]string, 0, docCount)
	for _, ref := range page1 {
		allResults = append(allResults, ref.ID)
	}

	// Fetch remaining pages
	cursor := page1[len(page1)-1].OrderKey
	for len(allResults) < docCount {
		plan.StartAfter = base64.RawURLEncoding.EncodeToString(cursor)
		page, err := svc.Search(ctx, "db1", plan)
		if err != nil {
			t.Fatalf("pagination failed: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, ref := range page {
			allResults = append(allResults, ref.ID)
		}
		cursor = page[len(page)-1].OrderKey
	}

	if len(allResults) != docCount {
		t.Fatalf("expected %d total results, got %d", docCount, len(allResults))
	}

	// Verify all unique and in order
	seen := make(map[string]bool)
	for i, id := range allResults {
		if seen[id] {
			t.Errorf("duplicate ID found: %s", id)
		}
		seen[id] = true

		// Verify order: user000 has highest timestamp (50), user049 has lowest (1)
		expected := fmt.Sprintf("user%03d", i)
		if id != expected {
			t.Errorf("result[%d]: expected %s, got %s", i, expected, id)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_HealthCheck(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Check health when running
	health, err := svc.Health(ctx)
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	if health.Status != indexer.HealthOK {
		t.Errorf("expected HealthOK, got %s", health.Status)
	}

	// Insert some events and check again
	for i := 0; i < 10; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%d", i), map[string]any{
			"timestamp": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	health, err = svc.Health(ctx)
	if err != nil {
		t.Fatalf("health check after events failed: %v", err)
	}

	if health.Status != indexer.HealthOK {
		t.Errorf("expected HealthOK after events, got %s", health.Status)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_StatsAccumulation(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(500)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Initial stats
	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("initial stats failed: %v", err)
	}
	if stats.EventsApplied != 0 {
		t.Errorf("expected 0 events initially, got %d", stats.EventsApplied)
	}

	// Insert events in batches and verify stats accumulation
	batches := []int{10, 25, 50, 15}
	totalEvents := int64(0)

	for _, batchSize := range batches {
		for i := 0; i < batchSize; i++ {
			evt := createTestEvent("db1", "users", fmt.Sprintf("user-%d-%d", len(batches), i), map[string]any{
				"timestamp": int64(i),
			})
			mockPullerSvc.pushEvent(evt, fmt.Sprintf("batch-%d", totalEvents))
			totalEvents++
		}

		waitIntegrationEvents(t, svc, mockPullerSvc)

		stats, err = svc.Stats(ctx)
		if err != nil {
			t.Fatalf("stats after batch failed: %v", err)
		}
		if stats.EventsApplied != totalEvents {
			t.Errorf("expected %d events after batch, got %d", totalEvents, stats.EventsApplied)
		}
	}

	if stats.EventsApplied != 100 {
		t.Errorf("expected 100 total events, got %d", stats.EventsApplied)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_AscendingOrder(t *testing.T) {
	t.Parallel()
	const docCount = 50
	mockPullerSvc := newMockPuller(docCount + 100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert products with prices
	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "products", fmt.Sprintf("prod%03d", i), map[string]any{
			"name":  fmt.Sprintf("Product %d", i),
			"price": float64(i * 10),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search products ordered by price ascending
	plan := indexer.Plan{
		Collection: "products",
		OrderBy:    []indexer.OrderField{{Field: "price", Direction: indexer.Asc}},
		Limit:      docCount,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != docCount {
		t.Fatalf("expected %d results, got %d", docCount, len(results))
	}

	// Verify ascending order: prod000 (price=0) should be first
	for i, ref := range results {
		expected := fmt.Sprintf("prod%03d", i)
		if ref.ID != expected {
			t.Errorf("result[%d]: expected %s, got %s", i, expected, ref.ID)
		}
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_DuplicateEventIdempotency(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert the same document multiple times (simulating redelivery)
	for i := 0; i < 5; i++ {
		evt := createTestEvent("db1", "users", "user001", map[string]any{
			"name":      "Alice",
			"timestamp": int64(1000 + i), // Different timestamp each time
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search - should only have one document
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      10,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result (deduped), got %d", len(results))
	}

	if results[0].ID != "user001" {
		t.Errorf("expected user001, got %s", results[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_EmptyCollection(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert some users but not orders
	for i := 0; i < 5; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%d", i), map[string]any{
			"timestamp": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search orders - template exists but no documents have been indexed yet
	// This returns empty results because no documents match
	plan := indexer.Plan{
		Collection: "orders",
		OrderBy:    []indexer.OrderField{{Field: "amount", Direction: indexer.Desc}},
		Limit:      10,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Errorf("expected no error for empty collection, got %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results for collection with no indexed documents, got %d", len(results))
	}

	// Now insert one order to create the index
	evt := createTestEvent("db1", "orders", "order001", map[string]any{
		"amount": float64(100),
	})
	mockPullerSvc.pushEvent(evt, "order-1")

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search again - should now work and return the one document
	results, err = svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed after inserting order: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("expected 1 result after inserting order, got %d", len(results))
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_LargeDocumentBatch(t *testing.T) {
	t.Parallel()
	const docCount = 5000
	mockPullerSvc := newMockPuller(docCount + 100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert 5000 documents rapidly
	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%05d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	// Wait for processing
	waitIntegrationEvents(t, svc, mockPullerSvc)

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

	// First result should be user04999 (highest timestamp)
	if results[0].ID != "user04999" {
		t.Errorf("expected user04999, got %s", results[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_TieBreakingByID(t *testing.T) {
	t.Parallel()
	const docCount = 100
	mockPullerSvc := newMockPuller(docCount + 100)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert documents with same timestamp (tests tie-breaking by ID)
	for i := 0; i < docCount; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(1000), // All same timestamp
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search - all should be returned, order determined by doc ID as tie-breaker
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      docCount,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) != docCount {
		t.Fatalf("expected %d results, got %d", docCount, len(results))
	}

	// All document IDs should be unique
	docIDs := make(map[string]bool)
	for _, ref := range results {
		if docIDs[ref.ID] {
			t.Errorf("duplicate doc ID found: %s", ref.ID)
		}
		docIDs[ref.ID] = true
	}

	// Results should be sorted by ID when timestamps are equal (btree uses ID as tie-breaker)
	ids := make([]string, len(results))
	for i, ref := range results {
		ids[i] = ref.ID
	}
	sortedIDs := make([]string, len(ids))
	copy(sortedIDs, ids)
	sort.Strings(sortedIDs)

	for i := range ids {
		if ids[i] != sortedIDs[i] {
			t.Errorf("result[%d]: expected %s, got %s (tie-breaker should sort by ID)", i, sortedIDs[i], ids[i])
		}
	}

	stopService(t, svc, mockPullerSvc)
}

// ============================================================================
// Dynamic Template Update Tests
// ============================================================================

func TestIntegration_DynamicTemplateReload(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(200)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert documents that match the initial "users" template
	for i := 0; i < 50; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
			"score":     int64(i * 10),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Verify initial template works (ordered by timestamp desc)
	plan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      10,
	}

	results, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("initial search failed: %v", err)
	}
	if len(results) != 10 {
		t.Fatalf("expected 10 results, got %d", len(results))
	}
	if results[0].ID != "user049" {
		t.Errorf("expected user049 first, got %s", results[0].ID)
	}

	// Rebuild with an additional template for ordering by score.
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

	// Reload templates via manager
	svc, ctx, cancel = reloadIntegrationTemplates(t, svc, mockPullerSvc, newTemplateYAML)
	defer cancel()

	// Continue ingesting after the complete rebuild.
	for i := 50; i < 100; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
			"score":     int64(i * 10),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("p%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search using the new score-based template
	scorePlan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "score", Direction: indexer.Desc}},
		Limit:      10,
	}

	scoreResults, err := svc.Search(ctx, "db1", scorePlan)
	if err != nil {
		t.Fatalf("score-based search failed: %v", err)
	}
	if len(scoreResults) != 10 {
		t.Fatalf("expected 10 results for score search, got %d", len(scoreResults))
	}
	// user099 has highest score (990)
	if scoreResults[0].ID != "user099" {
		t.Errorf("expected user099 first for score search, got %s", scoreResults[0].ID)
	}

	// Original timestamp-based search should still work
	timestampResults, err := svc.Search(ctx, "db1", plan)
	if err != nil {
		t.Fatalf("timestamp search after reload failed: %v", err)
	}
	if len(timestampResults) != 10 {
		t.Fatalf("expected 10 results for timestamp search, got %d", len(timestampResults))
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_TemplateAddNewCollection(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(200)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Initially, we have templates for users, orders, products, and chats
	// Let's add events for a collection that doesn't have a template yet

	// Insert events for "comments" collection - no template exists
	for i := 0; i < 20; i++ {
		evt := createTestEvent("db1", "comments", fmt.Sprintf("comment%03d", i), map[string]any{
			"text":      fmt.Sprintf("Comment %d", i),
			"createdAt": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("comment-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search comments - should fail with no matching index
	commentPlan := indexer.Plan{
		Collection: "comments",
		OrderBy:    []indexer.OrderField{{Field: "createdAt", Direction: indexer.Desc}},
		Limit:      10,
	}

	_, err := svc.Search(ctx, "db1", commentPlan)
	if err != indexer.ErrNoMatchingIndex {
		t.Errorf("expected ErrNoMatchingIndex for comments, got %v", err)
	}

	// Now add a template for comments
	newTemplateYAML := `
templates:
  - name: users_by_timestamp
    collectionPattern: "users"
    fields:
      - field: timestamp
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

  - name: comments_by_created
    collectionPattern: "comments"
    fields:
      - field: createdAt
        order: desc
`

	svc, ctx, cancel = reloadIntegrationTemplates(t, svc, mockPullerSvc, newTemplateYAML)
	defer cancel()

	// Insert more comments - these will be indexed with the new template
	for i := 20; i < 40; i++ {
		evt := createTestEvent("db1", "comments", fmt.Sprintf("comment%03d", i), map[string]any{
			"text":      fmt.Sprintf("Comment %d", i),
			"createdAt": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("comment-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// The rebuilt index includes earlier comments and subsequent events.
	results, err := svc.Search(ctx, "db1", commentPlan)
	if err != nil {
		t.Fatalf("search for comments failed after template add: %v", err)
	}
	if len(results) != 10 {
		t.Fatalf("expected 10 results, got %d", len(results))
	}
	// comment039 has highest createdAt
	if results[0].ID != "comment039" {
		t.Errorf("expected comment039 first, got %s", results[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_TemplateModifyFields(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(200)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert products with price and rating
	for i := 0; i < 50; i++ {
		evt := createTestEvent("db1", "products", fmt.Sprintf("prod%03d", i), map[string]any{
			"name":   fmt.Sprintf("Product %d", i),
			"price":  float64(i * 10),
			"rating": float64(5.0 - float64(i%5)*0.5), // ratings: 5.0, 4.5, 4.0, 3.5, 3.0
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("prod-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Initial template orders by price asc
	pricePlan := indexer.Plan{
		Collection: "products",
		OrderBy:    []indexer.OrderField{{Field: "price", Direction: indexer.Asc}},
		Limit:      10,
	}

	results, err := svc.Search(ctx, "db1", pricePlan)
	if err != nil {
		t.Fatalf("price search failed: %v", err)
	}
	if results[0].ID != "prod000" {
		t.Errorf("expected prod000 (lowest price) first, got %s", results[0].ID)
	}

	// Add a new template for rating-based sorting
	newTemplateYAML := `
templates:
  - name: users_by_timestamp
    collectionPattern: "users"
    fields:
      - field: timestamp
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

  - name: products_by_rating
    collectionPattern: "products"
    fields:
      - field: rating
        order: desc
      - field: price
        order: asc
`

	svc, ctx, cancel = reloadIntegrationTemplates(t, svc, mockPullerSvc, newTemplateYAML)
	defer cancel()

	// Continue ingesting products into the rebuilt rating index.
	for i := 50; i < 100; i++ {
		evt := createTestEvent("db1", "products", fmt.Sprintf("prod%03d", i), map[string]any{
			"name":   fmt.Sprintf("Product %d", i),
			"price":  float64(i * 10),
			"rating": float64(5.0 - float64(i%5)*0.5),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("prod-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search by rating desc, price asc
	ratingPlan := indexer.Plan{
		Collection: "products",
		OrderBy: []indexer.OrderField{
			{Field: "rating", Direction: indexer.Desc},
			{Field: "price", Direction: indexer.Asc},
		},
		Limit: 20,
	}

	ratingResults, err := svc.Search(ctx, "db1", ratingPlan)
	if err != nil {
		t.Fatalf("rating search failed: %v", err)
	}
	if len(ratingResults) != 20 {
		t.Fatalf("expected 20 results, got %d", len(ratingResults))
	}

	// Maintenance rebuild includes documents created before the template existed.
	if ratingResults[0].ID != "prod000" {
		t.Errorf("expected prod000 (rating 5.0, price 0) first, got %s", ratingResults[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_TemplateRemoveAndReAdd(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(200)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert users
	for i := 0; i < 30; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("user-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Verify users can be searched
	userPlan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      10,
	}

	results, err := svc.Search(ctx, "db1", userPlan)
	if err != nil {
		t.Fatalf("initial user search failed: %v", err)
	}
	if len(results) != 10 {
		t.Fatalf("expected 10 results, got %d", len(results))
	}

	// Remove the users template (load templates without it)
	reducedTemplateYAML := `
templates:
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

	svc, ctx, cancel = reloadIntegrationTemplates(t, svc, mockPullerSvc, reducedTemplateYAML)
	defer cancel()

	// Search for users should now fail with no matching index
	_, err = svc.Search(ctx, "db1", userPlan)
	if err != indexer.ErrNoMatchingIndex {
		t.Errorf("expected ErrNoMatchingIndex after template removal, got %v", err)
	}

	// Re-add the users template
	fullTemplateYAML := `
templates:
  - name: users_by_timestamp
    collectionPattern: "users"
    fields:
      - field: timestamp
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

	svc, ctx, cancel = reloadIntegrationTemplates(t, svc, mockPullerSvc, fullTemplateYAML)
	defer cancel()

	// Continue ingesting after rebuilding the restored template.
	for i := 30; i < 50; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      fmt.Sprintf("User %d", i),
			"timestamp": int64(i),
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("user-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Query the rebuilt index after subsequent insertions.
	results, err = svc.Search(ctx, "db1", userPlan)
	if err != nil {
		t.Fatalf("search after template re-add failed: %v", err)
	}
	if len(results) != 10 {
		t.Fatalf("expected 10 results after re-add, got %d", len(results))
	}
	// user049 has the highest timestamp.
	if results[0].ID != "user049" {
		t.Errorf("expected user049 first after re-add, got %s", results[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_MultipleTemplatesForSameCollection(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(500)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Load templates with multiple indexes for the same collection
	multiTemplateYAML := `
templates:
  - name: users_by_timestamp
    collectionPattern: "users"
    fields:
      - field: timestamp
        order: desc

  - name: users_by_name
    collectionPattern: "users"
    fields:
      - field: name
        order: asc

  - name: users_by_level_timestamp
    collectionPattern: "users"
    fields:
      - field: level
        order: desc
      - field: timestamp
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

	svc, ctx, cancel = reloadIntegrationTemplates(t, svc, mockPullerSvc, multiTemplateYAML)
	defer cancel()

	// Insert users with name, timestamp, and level
	names := []string{"Alice", "Bob", "Charlie", "David", "Eve"}
	for i := 0; i < 100; i++ {
		evt := createTestEvent("db1", "users", fmt.Sprintf("user%03d", i), map[string]any{
			"name":      names[i%5],
			"timestamp": int64(i),
			"level":     int64(i % 3), // levels: 0, 1, 2
		})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("user-%d", i))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search by timestamp desc
	timestampPlan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      10,
	}

	timestampResults, err := svc.Search(ctx, "db1", timestampPlan)
	if err != nil {
		t.Fatalf("timestamp search failed: %v", err)
	}
	if timestampResults[0].ID != "user099" {
		t.Errorf("timestamp search: expected user099 first, got %s", timestampResults[0].ID)
	}

	// Search by name asc
	namePlan := indexer.Plan{
		Collection: "users",
		OrderBy:    []indexer.OrderField{{Field: "name", Direction: indexer.Asc}},
		Limit:      10,
	}

	nameResults, err := svc.Search(ctx, "db1", namePlan)
	if err != nil {
		t.Fatalf("name search failed: %v", err)
	}
	// All results should start with "Alice" (lexicographically first)
	// Users with name "Alice": i % 5 == 0 -> user000, user005, user010, ...
	if nameResults[0].ID != "user000" {
		t.Errorf("name search: expected user000 first, got %s", nameResults[0].ID)
	}

	// Search by level desc, timestamp desc (compound index)
	levelPlan := indexer.Plan{
		Collection: "users",
		OrderBy: []indexer.OrderField{
			{Field: "level", Direction: indexer.Desc},
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 10,
	}

	levelResults, err := svc.Search(ctx, "db1", levelPlan)
	if err != nil {
		t.Fatalf("level search failed: %v", err)
	}
	// Level 2 users have highest level: i % 3 == 2 -> user002, user005, user008, ...
	// Among level 2 users, user098 has highest timestamp
	if levelResults[0].ID != "user098" {
		t.Errorf("level search: expected user098 first, got %s", levelResults[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}

func TestIntegration_TemplatePatternChange(t *testing.T) {
	t.Parallel()
	mockPullerSvc := newMockPuller(300)
	svc, ctx, cancel := setupIndexerService(t, mockPullerSvc)
	defer cancel()

	// Insert chats for different users
	for u := 0; u < 5; u++ {
		for c := 0; c < 20; c++ {
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

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search using the pattern template
	chatPlan := indexer.Plan{
		Collection: "users/user00/chats",
		OrderBy: []indexer.OrderField{
			{Field: "priority", Direction: indexer.Desc},
			{Field: "timestamp", Direction: indexer.Desc},
		},
		Limit: 20,
	}

	results, err := svc.Search(ctx, "db1", chatPlan)
	if err != nil {
		t.Fatalf("chat search failed: %v", err)
	}
	if len(results) != 20 {
		t.Fatalf("expected 20 results, got %d", len(results))
	}

	// Add a more specific pattern for user00's chats
	specificTemplateYAML := `
templates:
  - name: users_by_timestamp
    collectionPattern: "users"
    fields:
      - field: timestamp
        order: desc

  - name: user00_chats_by_timestamp
    collectionPattern: "users/user00/chats"
    fields:
      - field: timestamp
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

	svc, ctx, cancel = reloadIntegrationTemplates(t, svc, mockPullerSvc, specificTemplateYAML)
	defer cancel()

	// Insert more chats for user00
	for c := 20; c < 30; c++ {
		evt := createTestEvent("db1", "users/user00/chats",
			fmt.Sprintf("chat-u00-c%03d", c),
			map[string]any{
				"priority":  int64(c % 5),
				"timestamp": int64(c),
			})
		mockPullerSvc.pushEvent(evt, fmt.Sprintf("chat-specific-%d", c))
	}

	waitIntegrationEvents(t, svc, mockPullerSvc)

	// Search user00's chats with timestamp order - should use the more specific template
	specificPlan := indexer.Plan{
		Collection: "users/user00/chats",
		OrderBy:    []indexer.OrderField{{Field: "timestamp", Direction: indexer.Desc}},
		Limit:      10,
	}

	specificResults, err := svc.Search(ctx, "db1", specificPlan)
	if err != nil {
		t.Fatalf("specific pattern search failed: %v", err)
	}
	if len(specificResults) != 10 {
		t.Fatalf("expected 10 results for specific pattern, got %d", len(specificResults))
	}
	// chat-u00-c029 has the highest timestamp.
	if specificResults[0].ID != "chat-u00-c029" {
		t.Errorf("expected chat-u00-c029 first, got %s", specificResults[0].ID)
	}

	stopService(t, svc, mockPullerSvc)
}
