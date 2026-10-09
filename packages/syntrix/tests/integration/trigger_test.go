package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTriggerIntegration(t *testing.T) {
	t.Parallel()

	tc := NewTestContext(t)
	env := GetGlobalEnv()
	require.NotNil(t, env, "Global environment not initialized")
	require.NotNil(t, env.WebhookServer, "Webhook server not initialized")

	// Get a token for API requests
	token := tc.GetToken("trigger-test-user", "user")

	// Create a document that matches the trigger condition (age >= 18)
	collectionName := tc.Collection("trigger_users")
	docData := map[string]interface{}{
		"id":   fmt.Sprintf("user-%d", time.Now().UnixNano()),
		"name": "John Doe",
		"age":  20, // Matches condition >= 18
	}

	resp := tc.MakeRequest("POST",
		fmt.Sprintf("/api/v1/databases/default/documents/%s", collectionName),
		docData, token)
	require.Equal(t, http.StatusCreated, resp.StatusCode,
		"Failed to create document, status: %d", resp.StatusCode)

	// Wait for webhook delivery
	delivery, err := env.WebhookServer.WaitForDeliveryMatching("integration-test-trigger", collectionName, "create", 15*time.Second)
	require.NoError(t, err, "Failed to receive webhook delivery")
	require.NotNil(t, delivery, "Expected to receive a delivery")

	// Verify the delivery
	assert.Equal(t, "integration-test-trigger", delivery.TriggerID)
	assert.Equal(t, "create", delivery.Event)
	assert.Equal(t, collectionName, delivery.Collection)
	assert.NotEmpty(t, delivery.DocumentID)

	t.Logf("Successfully received webhook for trigger %s, document %s, collection %s",
		delivery.TriggerID, delivery.DocumentID, delivery.Collection)
}

func TestTriggerConditionNotMatched(t *testing.T) {
	t.Parallel()

	tc := NewTestContext(t)
	env := GetGlobalEnv()
	require.NotNil(t, env, "Global environment not initialized")
	require.NotNil(t, env.WebhookServer, "Webhook server not initialized")

	// Get a token for API requests
	token := tc.GetToken("trigger-nomatch-user", "user")

	// Create a document that does NOT match the trigger condition (age < 18)
	collectionName := tc.Collection("trigger_young_users")
	docData := map[string]interface{}{
		"id":   fmt.Sprintf("user-%d", time.Now().UnixNano()),
		"name": "Young User",
		"age":  15, // Does NOT match condition >= 18
	}

	resp := tc.MakeRequest("POST",
		fmt.Sprintf("/api/v1/databases/default/documents/%s", collectionName),
		docData, token)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// Wait a bit and verify webhook was NOT called for this collection
	time.Sleep(2 * time.Second)

	hasDelivery := env.WebhookServer.HasDeliveryForCollection(collectionName)
	assert.False(t, hasDelivery, "Webhook should NOT have been called for document not matching condition")
}

func TestTriggerUpdateEvent(t *testing.T) {
	t.Parallel()

	tc := NewTestContext(t)
	env := GetGlobalEnv()
	require.NotNil(t, env, "Global environment not initialized")
	require.NotNil(t, env.WebhookServer, "Webhook server not initialized")

	// Get a token for API requests
	token := tc.GetToken("trigger-update-user", "user")

	// First create a document that does NOT match the condition
	collectionName := tc.Collection("trigger_update_users")
	docData := map[string]interface{}{
		"name": "Test User",
		"age":  15, // Does NOT match condition >= 18
	}

	resp := tc.MakeRequest("POST",
		fmt.Sprintf("/api/v1/databases/default/documents/%s", collectionName),
		docData, token)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// Parse response to get the document ID
	var createdDoc map[string]interface{}
	err := json.NewDecoder(resp.Body).Decode(&createdDoc)
	require.NoError(t, err)
	docID, ok := createdDoc["id"].(string)
	require.True(t, ok, "Expected id field in response")

	// Wait a bit to ensure no webhook was triggered for create
	time.Sleep(1 * time.Second)

	// Now update the document to match the condition
	// API expects: {"doc": {...update fields...}}
	updateData := map[string]interface{}{
		"doc": map[string]interface{}{
			"age": 25, // Now matches condition >= 18
		},
	}

	resp = tc.MakeRequest("PATCH",
		fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", collectionName, docID),
		updateData, token)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Wait for webhook delivery for the update event
	delivery, err := env.WebhookServer.WaitForDeliveryMatching("integration-test-trigger", collectionName, "update", 15*time.Second)
	require.NoError(t, err, "Failed to receive webhook delivery for update")
	require.NotNil(t, delivery, "Expected to receive a delivery for update")

	// Verify the delivery
	assert.Equal(t, "integration-test-trigger", delivery.TriggerID)
	assert.Equal(t, "update", delivery.Event)
	assert.Equal(t, collectionName, delivery.Collection)

	t.Logf("Successfully received webhook for update event: trigger %s, document %s",
		delivery.TriggerID, delivery.DocumentID)
}
