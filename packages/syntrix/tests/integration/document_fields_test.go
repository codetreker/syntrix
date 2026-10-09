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

func TestDocumentSystemFields(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	token := env.GetToken(t, "test-user", "user")
	collection := "test_fields"

	// 1. Create Document
	docData := map[string]interface{}{
		"field1": "value1",
	}

	createdDoc := env.CreateDocument(t, collection, docData, token)

	// Verify System Fields on Create
	assert.NotEmpty(t, createdDoc["id"])
	docID := createdDoc["id"].(string)

	assert.Equal(t, collection, createdDoc["collection"])
	assert.Equal(t, float64(1), createdDoc["version"]) // JSON numbers are floats
	assert.NotEmpty(t, createdDoc["createdAt"])
	assert.NotEmpty(t, createdDoc["updatedAt"])

	createdAt := createdDoc["createdAt"].(float64)
	updatedAt := createdDoc["updatedAt"].(float64)
	assert.Equal(t, createdAt, updatedAt, "On create, createdAt should equal updatedAt")

	// Sleep to ensure timestamp difference
	time.Sleep(10 * time.Millisecond)

	// 2. Update Document (PATCH)
	patchData := map[string]interface{}{
		"doc": map[string]interface{}{
			"field2": "value2",
		},
	}
	patchedDoc := env.PatchDocument(t, collection, docID, patchData, token)

	// Verify fields are merged (PATCH behavior)
	assert.Equal(t, "value1", patchedDoc["field1"], "Existing field should be preserved")
	assert.Equal(t, "value2", patchedDoc["field2"], "New field should be added")

	// Verify System Fields on Update
	assert.Equal(t, float64(2), patchedDoc["version"])
	assert.Equal(t, createdAt, patchedDoc["createdAt"].(float64), "createdAt should not change")
	assert.Greater(t, patchedDoc["updatedAt"].(float64), updatedAt, "updatedAt should increase")

	updatedAtAfterPatch := patchedDoc["updatedAt"].(float64)

	// Sleep to ensure timestamp difference
	time.Sleep(10 * time.Millisecond)

	// 3. Replace Document (PUT)
	replaceData := map[string]interface{}{
		"doc": map[string]interface{}{
			"field1": "value1-replaced",
			"field3": "value3",
		},
	}
	replacedDoc := env.PutDocument(t, collection, docID, replaceData, token)

	// Verify System Fields on Replace
	assert.Equal(t, float64(3), replacedDoc["version"])
	assert.Equal(t, createdAt, replacedDoc["createdAt"].(float64), "createdAt should not change")
	assert.Greater(t, replacedDoc["updatedAt"].(float64), updatedAtAfterPatch, "updatedAt should increase")

	// 4. Verify Shadow Fields Protection (Try to write them)
	maliciousData := map[string]interface{}{
		"doc": map[string]interface{}{
			"field4":     "value4",
			"version":    999,
			"createdAt":  0,
			"collection": "hacked",
		},
	}
	resp := env.MakeRequest(t, "PATCH", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", collection, docID), maliciousData, token)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var protectedDoc map[string]interface{}
	err := json.NewDecoder(resp.Body).Decode(&protectedDoc)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, float64(4), protectedDoc["version"], "Server should ignore client version and increment its own")
	assert.Equal(t, createdAt, protectedDoc["createdAt"].(float64), "Server should ignore client createdAt")
	assert.Equal(t, collection, protectedDoc["collection"], "Server should ignore client collection")
}
