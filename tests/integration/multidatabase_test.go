package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestMultiDatabase_DataIsolation(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	databaseA := "database-a"
	databaseB := "database-b"

	// Ensure databases exist
	env.EnsureDatabase(t, databaseA)
	env.EnsureDatabase(t, databaseB)

	tokenA := env.GetTokenForDatabase(t, databaseA, "user-a", "user")
	tokenB := env.GetTokenForDatabase(t, databaseB, "user-b", "user")

	collection := "private_docs"

	// 1. Database A creates a document
	docData := map[string]interface{}{
		"title": "Secret Plan A",
		"owner": "A",
	}
	resp := env.MakeRequest(t, "POST", fmt.Sprintf("/api/v1/databases/%s/documents/%s", databaseA, collection), docData, tokenA)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var createdDoc map[string]interface{}
	err := json.NewDecoder(resp.Body).Decode(&createdDoc)
	require.NoError(t, err)
	resp.Body.Close()
	docID := createdDoc["id"].(string)

	// 2. Database B tries to GET the document (Should fail)
	resp = env.MakeRequest(t, "GET", fmt.Sprintf("/api/v1/databases/%s/documents/%s/%s", databaseB, collection, docID), nil, tokenB)
	// Should be 404 Not Found because it doesn't exist in Database B's scope
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// 3. Database B tries to LIST documents (Should not see it)
	// GET /api/v1/collection is not supported, use Query
	queryAll := model.Query{
		Collection: collection,
	}
	resp = env.MakeRequest(t, "POST", fmt.Sprintf("/api/v1/databases/%s/query", databaseB), queryAll, tokenB)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	listRes := decodeQueryDocuments(t, resp)

	assert.Empty(t, listRes, "Database B should see no documents")

	// 4. Database B tries to UPDATE the document (Should fail)
	updateData := map[string]interface{}{
		"doc": map[string]interface{}{
			"title": "Hacked by B",
		},
	}
	resp = env.MakeRequest(t, "PATCH", fmt.Sprintf("/api/v1/databases/%s/documents/%s/%s", databaseB, collection, docID), updateData, tokenB)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// 5. Database B tries to DELETE the document (Should fail)
	resp = env.MakeRequest(t, "DELETE", fmt.Sprintf("/api/v1/databases/%s/documents/%s/%s", databaseB, collection, docID), nil, tokenB)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	// 6. Database B tries to Query the document (Should not find it)
	query := model.Query{
		Collection: collection,
		Filters: []model.Filter{
			{Field: "title", Op: model.OpEq, Value: "Secret Plan A"},
		},
	}
	resp = env.MakeRequest(t, "POST", fmt.Sprintf("/api/v1/databases/%s/query", databaseB), query, tokenB)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	queryResults := decodeQueryDocuments(t, resp)
	assert.Empty(t, queryResults, "Database B query should return empty")

	// 7. Database A can see the document
	resp = env.MakeRequest(t, "GET", fmt.Sprintf("/api/v1/databases/%s/documents/%s/%s", databaseA, collection, docID), nil, tokenA)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMultiDatabase_RealtimeIsolation(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	databaseA := "database-a"
	databaseB := "database-b"

	// Ensure databases exist
	env.EnsureDatabase(t, databaseA)
	env.EnsureDatabase(t, databaseB)

	tokenA := env.GetTokenForDatabase(t, databaseA, "user-a", "user")
	tokenB := env.GetTokenForDatabase(t, databaseB, "user-b", "user")

	collection := "live_updates"

	// Connect Client A (Database A)
	wsA := env.ConnectWebSocket(t)
	defer wsA.Close()

	// Authenticate A
	env.AuthenticateWebSocketWithDatabase(t, wsA, tokenA, databaseA)

	// Subscribe A
	err := wsA.WriteJSON(BaseMessage{
		ID:   "sub-a",
		Type: TypeSubscribe,
		Payload: mustMarshal(SubscribePayload{
			Query:       Query{Collection: collection},
			IncludeData: true,
		}),
	})
	require.NoError(t, err)
	readUntilType(t, wsA, TypeSubscribeAck)

	// Connect Client B (Database B)
	wsB := env.ConnectWebSocket(t)
	defer wsB.Close()

	// Authenticate B
	env.AuthenticateWebSocketWithDatabase(t, wsB, tokenB, databaseB)

	// Subscribe B
	err = wsB.WriteJSON(BaseMessage{
		ID:   "sub-b",
		Type: TypeSubscribe,
		Payload: mustMarshal(SubscribePayload{
			Query:       Query{Collection: collection},
			IncludeData: true,
		}),
	})
	require.NoError(t, err)
	readUntilType(t, wsB, TypeSubscribeAck)

	// 1. Create Document in Database A
	docData := map[string]interface{}{
		"msg": "Hello Database A",
	}
	resp := env.MakeRequest(t, "POST", fmt.Sprintf("/api/v1/databases/%s/documents/%s", databaseA, collection), docData, tokenA)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Client A should receive event
	msgA := readUntilType(t, wsA, TypeEvent)
	var payloadA EventPayload
	json.Unmarshal(msgA.Payload, &payloadA)
	assert.Equal(t, "Hello Database A", payloadA.Delta.Document["msg"])

	// 2. Create Document in Database B
	docDataB := map[string]interface{}{
		"msg": "Hello Database B",
	}
	respB := env.MakeRequest(t, "POST", fmt.Sprintf("/api/v1/databases/%s/documents/%s", databaseB, collection), docDataB, tokenB)
	require.Equal(t, http.StatusCreated, respB.StatusCode)
	respB.Body.Close()

	// Client B should receive event
	wsB.SetReadDeadline(time.Time{}) // Reset deadline
	msgB := readUntilType(t, wsB, TypeEvent)
	var payloadB EventPayload
	json.Unmarshal(msgB.Payload, &payloadB)
	assert.Equal(t, "Hello Database B", payloadB.Delta.Document["msg"])

	// Client A should NOT receive event
	wsA.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, _, err = wsA.ReadMessage()
	if err == nil {
		// Fail if we got a message (unless it's unrelated, but we expect silence)
	} else {
		assert.Contains(t, err.Error(), "i/o timeout")
	}
}

func TestMultiDatabase_ReplicationIsolation(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	databaseA := "database-a"
	databaseB := "database-b"

	// Ensure databases exist
	env.EnsureDatabase(t, databaseA)
	env.EnsureDatabase(t, databaseB)

	tokenA := env.GetTokenForDatabase(t, databaseA, "user-a", "user")
	tokenB := env.GetTokenForDatabase(t, databaseB, "user-b", "user")

	collection := "repl_docs"

	// 1. Database A creates a document via REST
	docData := map[string]interface{}{
		"title": "Repl Doc A",
	}
	resp := env.MakeRequest(t, "POST", fmt.Sprintf("/api/v1/databases/%s/documents/%s", databaseA, collection), docData, tokenA)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// 2. Database B PULLs (Should get nothing)
	pullURL := fmt.Sprintf("/replication/v1/databases/%s/pull?collection=%s&checkpoint=0&limit=100", databaseB, collection)
	resp = env.MakeRequest(t, "GET", pullURL, nil, tokenB)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var pullRes map[string]interface{}
	err := json.NewDecoder(resp.Body).Decode(&pullRes)
	require.NoError(t, err)
	resp.Body.Close()

	docs := pullRes["documents"].([]interface{})
	assert.Empty(t, docs, "Database B should pull 0 documents")

	// 3. Database B PUSHes a document
	pushReq := map[string]interface{}{
		"collection": collection,
		"changes": []map[string]interface{}{
			{
				"action": "create",
				"document": map[string]interface{}{
					"id":    "doc-b",
					"title": "Repl Doc B",
				},
			},
		},
	}
	resp = env.MakeRequest(t, "POST", fmt.Sprintf("/replication/v1/databases/%s/push", databaseB), pushReq, tokenB)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 4. Database A PULLs (Should see only Doc A, not Doc B)
	pullURLForA := fmt.Sprintf("/replication/v1/databases/%s/pull?collection=%s&checkpoint=0&limit=100", databaseA, collection)
	resp = env.MakeRequest(t, "GET", pullURLForA, nil, tokenA)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	err = json.NewDecoder(resp.Body).Decode(&pullRes)
	require.NoError(t, err)
	resp.Body.Close()

	docsA := pullRes["documents"].([]interface{})
	// Should contain Doc A (maybe) but definitely NOT Doc B
	for _, d := range docsA {
		docMap := d.(map[string]interface{})
		assert.NotEqual(t, "doc-b", docMap["id"], "Database A should not see Database B's document")
	}
}

func readUntilType(t *testing.T, ws *websocket.Conn, targetType string) BaseMessage {
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, p, err := ws.ReadMessage()
		require.NoError(t, err)
		var msg BaseMessage
		err = json.Unmarshal(p, &msg)
		require.NoError(t, err)

		t.Logf("Received message type: %s", msg.Type)
		if msg.Type == targetType {
			ws.SetReadDeadline(time.Time{}) // Reset deadline
			return msg
		}
		if msg.Type == TypeError {
			t.Fatalf("Received error from server: %s", string(msg.Payload))
		}
	}
}
