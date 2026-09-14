package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/syntrixbase/syntrix/internal/gateway/realtime"
	"github.com/syntrixbase/syntrix/internal/gateway/rest"
	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplication_FullFlow(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	// Get Token
	token := env.GetToken(t, "user1", "user")

	database := "default"
	token = replicationAdminToken(t, env, database, token)
	collectionName := env.testPrefix + "_replication"

	// 1. Setup Realtime (SSE) Connection
	sseURL := fmt.Sprintf("%s/realtime/sse?database=%s&collection=%s", env.RealtimeURL, database, collectionName)
	req, err := http.NewRequest("GET", sseURL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", env.RealtimeURL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	reader := bufio.NewReader(resp.Body)

	readLine := func() string {
		type result struct {
			line string
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			line, err := reader.ReadString('\n')
			ch <- result{line, err}
		}()
		select {
		case res := <-ch:
			require.NoError(t, res.err)
			return res.line
		case <-time.After(5 * time.Second):
			t.Fatal("Timeout reading SSE stream")
			return ""
		}
	}

	// Read connection message
	line := readLine()
	assert.Equal(t, ": connected\n", line)

	// Wait for subscription to be registered
	time.Sleep(100 * time.Millisecond)

	// 2. Setup event listener BEFORE Push (events are async and may arrive quickly)
	done := make(chan bool)
	docID := "doc-1"
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				dataStr := strings.TrimPrefix(line, "data: ")
				dataStr = strings.TrimSpace(dataStr)

				var msg realtime.BaseMessage
				err = json.Unmarshal([]byte(dataStr), &msg)
				if err == nil && msg.Type == realtime.TypeEvent {
					var eventPayload realtime.EventPayload
					if err := json.Unmarshal(msg.Payload, &eventPayload); err == nil {
						// Check if it matches our document
						if eventPayload.Delta.Document["id"] == docID {
							done <- true
							return
						}
					}
				}
			}
		}
	}()

	// 3. Push a Document
	docData := map[string]interface{}{
		"id":      docID,
		"msg":     "hello replication",
		"version": float64(0), // New document
	}

	pushBody := rest.ReplicaPushRequest{
		Collection: collectionName,
		Changes: []rest.ReplicaChange{
			{
				Doc: docData,
			},
		},
	}

	bodyBytes, _ := json.Marshal(pushBody)
	pushURL := fmt.Sprintf("%s/replication/v1/databases/%s/push", env.APIURL, database)

	pushReq, err := http.NewRequest("POST", pushURL, bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	pushReq.Header.Set("Content-Type", "application/json")
	pushReq.Header.Set("Authorization", "Bearer "+token)

	pushResp, err := client.Do(pushReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, pushResp.StatusCode)

	var pushResult map[string]interface{}
	json.NewDecoder(pushResp.Body).Decode(&pushResult)
	pushResp.Body.Close()

	// Expect no conflicts
	conflicts := pushResult["conflicts"].([]interface{})
	assert.Empty(t, conflicts)

	// 4. Wait for Realtime Event
	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for SSE event")
	}

	pullResult := pullReplicationPage(t, env, database, collectionName, "", 100, token)
	assert.NotEmpty(t, pullResult.Documents)
	found := false
	for _, doc := range pullResult.Documents {
		if doc["id"] == docID {
			assert.Equal(t, "hello replication", doc["msg"])
			found = true
			break
		}
	}
	assert.True(t, found, "Document should be returned in pull")

	// 5. Scenario: Delete Document
	deleteDocData := map[string]interface{}{
		"id":      docID,
		"version": float64(1),
	}

	deleteBody := rest.ReplicaPushRequest{
		Collection: collectionName,
		Changes: []rest.ReplicaChange{
			{
				Action: "delete",
				Doc:    deleteDocData,
			},
		},
	}

	deleteBytes, _ := json.Marshal(deleteBody)
	deleteReq, err := http.NewRequest("POST", pushURL, bytes.NewBuffer(deleteBytes))
	require.NoError(t, err)
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteReq.Header.Set("Authorization", "Bearer "+token)

	deleteResp, err := client.Do(deleteReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, deleteResp.StatusCode)
	deleteResp.Body.Close()

	var deleted model.Document
	for pageNumber := 0; pageNumber < 20; pageNumber++ {
		pullResult = pullReplicationPage(t, env, database, collectionName, pullResult.Checkpoint, 100, token)
		for _, doc := range pullResult.Documents {
			if doc.GetID() == docID && doc["deleted"] == true {
				deleted = doc
			}
		}
		if pullResult.CaughtUp {
			break
		}
	}
	require.True(t, pullResult.CaughtUp, "replication must reach a source watermark")
	require.NotNil(t, deleted, "deleted document must be returned in pull")
	assert.Equal(t, collectionName, deleted.GetCollection())
	assert.NotContains(t, deleted, "msg")
}
