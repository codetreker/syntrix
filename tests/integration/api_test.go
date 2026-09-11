package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIIntegration runs a full integration test against a real MongoDB.
// It requires MongoDB to be running (e.g. via docker-compose).
func TestAPIIntegration(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	token := env.GetToken(t, "test-user", "user")
	collection := "rooms/room-1/messages"

	// 3. Scenario: Create Document
	docData := map[string]interface{}{
		"name": "Integration User",
		"age":  42,
	}

	createdDoc := env.CreateDocument(t, collection, docData, token)

	assert.NotEmpty(t, createdDoc["id"])
	// Collection is not returned in flattened response
	// assert.Equal(t, collection, createdDoc.Collection)
	assert.Equal(t, "Integration User", createdDoc["name"])

	docID := createdDoc["id"].(string)

	// Note: Internal Query Service verification removed - Query now uses gRPC
	// The document is already verified via the API gateway which uses the local query service

	// 4. Scenario: Get Document
	fetchedDoc := env.GetDocument(t, collection, docID, token)

	assert.Equal(t, createdDoc["id"], fetchedDoc["id"])
	assert.Equal(t, float64(42), fetchedDoc["age"]) // JSON numbers are floats

	// 4.5 Scenario: Patch Document
	patchData := map[string]interface{}{
		"doc": map[string]interface{}{
			"age": 43,
		},
	}
	patchedDoc := env.PatchDocument(t, collection, docID, patchData, token)

	assert.Equal(t, "Integration User", patchedDoc["name"]) // Should remain unchanged
	assert.Equal(t, float64(43), patchedDoc["age"])         // Should be updated

	// 4.6 Scenario: Replace Document (PUT)
	replaceData := map[string]interface{}{
		"doc": map[string]interface{}{
			"name":   "Replaced User",
			"status": "active",
		},
	}
	replacedDoc := env.PutDocument(t, collection, docID, replaceData, token)

	assert.Equal(t, "Replaced User", replacedDoc["name"])
	assert.Equal(t, "active", replacedDoc["status"])
	assert.Nil(t, replacedDoc["age"]) // age should be gone after replace

	// 5. Scenario: Query Document
	time.Sleep(1 * time.Second) // Wait for indexer to digest changes
	query := model.Query{
		Collection: collection,
		Filters: []model.Filter{
			{Field: "name", Op: model.OpEq, Value: "Replaced User"},
		},
	}
	resp := env.MakeRequest(t, "POST", "/api/v1/databases/default/query", query, token)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	queryResults := decodeQueryDocuments(t, resp)

	found := false
	for _, d := range queryResults {
		if d["id"] == createdDoc["id"] {
			found = true
			break
		}
	}
	assert.True(t, found, "Created document should be found in query")

	// 5.5 Scenario: Conditional Update (IfMatch)
	// First update to set a known state
	patchData = map[string]interface{}{
		"doc": map[string]interface{}{
			"age": 43,
		},
	}
	env.PatchDocument(t, collection, docID, patchData, token)

	// Success case
	ifMatchData := map[string]interface{}{
		"doc": map[string]interface{}{
			"age": 44,
		},
		"ifMatch": []map[string]interface{}{
			{"field": "age", "op": "==", "value": 43},
		},
	}
	resp = env.MakeRequest(t, "PATCH", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", collection, docID), ifMatchData, token)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Failure case (Condition not met)
	ifMatchFailData := map[string]interface{}{
		"doc": map[string]interface{}{
			"age": 45,
		},
		"ifMatch": []map[string]interface{}{
			{"field": "age", "op": "==", "value": 999}, // Wrong age
		},
	}
	resp = env.MakeRequest(t, "PATCH", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", collection, docID), ifMatchFailData, token)
	require.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
	resp.Body.Close()

	// 6. Scenario: Delete Document
	resp = env.MakeRequest(t, "DELETE", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", collection, docID), nil, token)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	// Verify Delete
	resp = env.MakeRequest(t, "GET", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", collection, docID), nil, token)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

// TestAPIQueryAdvanced tests advanced query features
func TestAPIQueryAdvanced(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	token := env.GetToken(t, "query-user", "user")
	collection := "products"

	// Create test documents
	docs := []map[string]interface{}{
		{"name": "Apple", "price": 1.50, "category": "fruit", "stock": 100},
		{"name": "Banana", "price": 0.75, "category": "fruit", "stock": 200},
		{"name": "Carrot", "price": 0.50, "category": "vegetable", "stock": 150},
		{"name": "Milk", "price": 2.00, "category": "dairy", "stock": 50},
		{"name": "Cheese", "price": 5.00, "category": "dairy", "stock": 30},
	}

	var createdIDs []string
	for _, doc := range docs {
		created := env.CreateDocument(t, collection, doc, token)
		createdIDs = append(createdIDs, created["id"].(string))
	}

	time.Sleep(1 * time.Second) // Wait for indexer to digest changes

	// Test 1: Query with multiple filters (AND logic)
	t.Run("MultipleFilters", func(t *testing.T) {
		query := model.Query{
			Collection: collection,
			Filters: []model.Filter{
				{Field: "category", Op: model.OpEq, Value: "fruit"},
				{Field: "price", Op: model.OpLt, Value: 1.0},
			},
		}
		resp := env.MakeRequest(t, "POST", "/api/v1/databases/default/query", query, token)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		results := decodeQueryDocuments(t, resp)

		// Should only match Banana (fruit with price < 1.0)
		require.Len(t, results, 1)
		assert.Equal(t, "Banana", results[0]["name"])
	})

	// Test 2: Query with OrderBy
	t.Run("OrderBy", func(t *testing.T) {
		query := model.Query{
			Collection: collection,
			Filters: []model.Filter{
				{Field: "category", Op: model.OpEq, Value: "dairy"},
			},
			OrderBy: []model.Order{
				{Field: "price", Direction: "desc"},
			},
		}
		resp := env.MakeRequest(t, "POST", "/api/v1/databases/default/query", query, token)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		results := decodeQueryDocuments(t, resp)

		require.Len(t, results, 2)
		// Cheese ($5) should come before Milk ($2)
		assert.Equal(t, "Cheese", results[0]["name"])
		assert.Equal(t, "Milk", results[1]["name"])
	})

	// Test 3: Query with Limit
	t.Run("Limit", func(t *testing.T) {
		query := model.Query{
			Collection: collection,
			Limit:      2,
		}
		resp := env.MakeRequest(t, "POST", "/api/v1/databases/default/query", query, token)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		results := decodeQueryDocuments(t, resp)

		assert.Len(t, results, 2)
	})

	// Test 4: Query with different operators
	t.Run("DifferentOperators", func(t *testing.T) {
		// Greater than or equal
		query := model.Query{
			Collection: collection,
			Filters: []model.Filter{
				{Field: "stock", Op: model.OpGte, Value: 150},
			},
		}
		resp := env.MakeRequest(t, "POST", "/api/v1/databases/default/query", query, token)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		results := decodeQueryDocuments(t, resp)

		// Apple (100), Banana (200), Carrot (150) - only Banana and Carrot have stock >= 150
		assert.Len(t, results, 2)
	})

	// Test 5: Query ShowDeleted
	t.Run("ShowDeleted", func(t *testing.T) {
		// Delete one document
		resp := env.MakeRequest(t, "DELETE", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", collection, createdIDs[0]), nil, token)
		require.Equal(t, http.StatusNoContent, resp.StatusCode)
		resp.Body.Close()

		// Query without ShowDeleted - should not include deleted doc
		query := model.Query{
			Collection:  collection,
			ShowDeleted: false,
		}
		resp = env.MakeRequest(t, "POST", "/api/v1/databases/default/query", query, token)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		results := decodeQueryDocuments(t, resp)

		assert.Len(t, results, 4) // 5 - 1 deleted

		// Query with ShowDeleted - should include deleted doc
		query.ShowDeleted = true
		resp = env.MakeRequest(t, "POST", "/api/v1/databases/default/query", query, token)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		results = decodeQueryDocuments(t, resp)

		assert.Len(t, results, 5) // All 5 including deleted
	})
}

// TestAPIErrorCases tests various error scenarios
func TestAPIErrorCases(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	token := env.GetToken(t, "error-user", "user")
	collection := "errors"

	// Test 1: Get non-existent document (404)
	t.Run("NotFound", func(t *testing.T) {
		resp := env.MakeRequest(t, "GET", fmt.Sprintf("/api/v1/databases/default/documents/%s/nonexistent-id", collection), nil, token)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	})

	// Test 2: Invalid request body (400)
	t.Run("BadRequest", func(t *testing.T) {
		// Send invalid JSON
		req, _ := http.NewRequest("POST", env.APIURL+"/api/v1/databases/default/documents/"+collection, strings.NewReader("{invalid json}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	// Test 3: Unauthorized request (401)
	t.Run("Unauthorized", func(t *testing.T) {
		resp := env.MakeRequest(t, "GET", fmt.Sprintf("/api/v1/databases/default/documents/%s/some-id", collection), nil, "invalid-token")
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		resp.Body.Close()
	})

	// Test 4: Delete non-existent document (404)
	t.Run("DeleteNotFound", func(t *testing.T) {
		resp := env.MakeRequest(t, "DELETE", fmt.Sprintf("/api/v1/databases/default/documents/%s/nonexistent-id", collection), nil, token)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	})

	// Test 5: Invalid query (400)
	t.Run("InvalidQuery", func(t *testing.T) {
		invalidQuery := map[string]interface{}{
			"filters": []map[string]interface{}{
				{"field": "name", "op": "invalid_op", "value": "test"},
			},
		}
		resp := env.MakeRequest(t, "POST", "/api/v1/databases/default/query", invalidQuery, token)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		resp.Body.Close()
	})

	// Test 6: Patch non-existent document (404)
	t.Run("PatchNotFound", func(t *testing.T) {
		patchData := map[string]interface{}{
			"doc": map[string]interface{}{
				"name": "updated",
			},
		}
		resp := env.MakeRequest(t, "PATCH", fmt.Sprintf("/api/v1/databases/default/documents/%s/nonexistent-id", collection), patchData, token)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	})

	// Test 7: PUT creates document if not exists (upsert behavior)
	t.Run("PutCreatesIfNotExists", func(t *testing.T) {
		newDocID := "new-doc-via-put"
		putData := map[string]interface{}{
			"doc": map[string]interface{}{
				"name":   "Created via PUT",
				"status": "new",
			},
		}
		resp := env.MakeRequest(t, "PUT", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", collection, newDocID), putData, token)
		// PUT should create if not exists (upsert)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()

		// Verify document was created
		doc := env.GetDocument(t, collection, newDocID, token)
		assert.Equal(t, "Created via PUT", doc["name"])
		assert.Equal(t, "new", doc["status"])
	})

	// Test 8: Replace existing document (PUT)
	t.Run("PutReplacesExisting", func(t *testing.T) {
		// First create a document
		createData := map[string]interface{}{
			"name":  "Original Name",
			"field": "original",
		}
		created := env.CreateDocument(t, collection, createData, token)
		createdID := created["id"].(string)

		// Now replace it with PUT
		putData := map[string]interface{}{
			"doc": map[string]interface{}{
				"name":   "Replaced Name",
				"status": "replaced",
			},
		}
		resp := env.MakeRequest(t, "PUT", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", collection, createdID), putData, token)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()

		// Verify replacement
		doc := env.GetDocument(t, collection, createdID, token)
		assert.Equal(t, "Replaced Name", doc["name"])
		assert.Equal(t, "replaced", doc["status"])
		assert.Nil(t, doc["field"]) // original field should be gone
	})
}
