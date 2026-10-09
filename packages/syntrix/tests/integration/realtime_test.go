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

	"github.com/codetreker/syntrix/pkg/model"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealtime_FullFlow(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	token := env.GetToken(t, "realtime-user", "user")

	// Connect and Authenticate
	ws := env.ConnectWebSocket(t)
	defer ws.Close()
	env.AuthenticateWebSocket(t, ws, token)

	// 2. Subscribe
	collectionName := "realtime_test_col"
	subID := "sub-1"
	subMsg := BaseMessage{
		ID:   subID,
		Type: TypeSubscribe,
		Payload: mustMarshal(SubscribePayload{
			Query: Query{
				Collection: collectionName,
			},
			IncludeData: true,
		}),
	}
	err := ws.WriteJSON(subMsg)
	require.NoError(t, err)

	// Give some time for subscription to register
	time.Sleep(50 * time.Millisecond)

	// 2.5 Receive Subscribe Ack
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var subAckMsg BaseMessage
	err = ws.ReadJSON(&subAckMsg)
	require.NoError(t, err, "Should receive subscribe ack")
	assert.Equal(t, TypeSubscribeAck, subAckMsg.Type)
	assert.Equal(t, subID, subAckMsg.ID)

	// 3. Trigger Event (Create Document via API Gateway)
	docData := map[string]interface{}{
		"msg": "hello realtime",
	}
	body, _ := json.Marshal(docData)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/databases/default/documents/%s", env.APIURL, collectionName), bytes.NewBuffer(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// 4. Receive Event
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var eventMsg BaseMessage
	err = ws.ReadJSON(&eventMsg)
	require.NoError(t, err, "Should receive event message")

	assert.Equal(t, TypeEvent, eventMsg.Type)

	var eventPayload EventPayload
	err = json.Unmarshal(eventMsg.Payload, &eventPayload)
	require.NoError(t, err)

	assert.Equal(t, subID, eventPayload.SubID)
	assert.Equal(t, EventCreate, eventPayload.Delta.Type)
	// Path is not in flattened document, but it is in Delta.Path
	// assert.Equal(t, collectionName, eventPayload.Delta.Document.Collection)
	assert.Equal(t, "hello realtime", eventPayload.Delta.Document["msg"])

	// 5. Unsubscribe
	unsubMsg := BaseMessage{
		ID:   "unsub-1",
		Type: TypeUnsubscribe,
		Payload: mustMarshal(UnsubscribePayload{
			ID: subID,
		}),
	}
	err = ws.WriteJSON(unsubMsg)
	require.NoError(t, err)

	// Read Unsubscribe Ack (may need to skip events that arrive before the ack)
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var unsubAckMsg BaseMessage
	for {
		err = ws.ReadJSON(&unsubAckMsg)
		require.NoError(t, err)
		if unsubAckMsg.Type == TypeUnsubscribeAck {
			break
		}
		// Skip any events that arrive before the unsubscribe ack
		t.Logf("Skipping message type=%s while waiting for unsubscribe_ack", unsubAckMsg.Type)
	}
	assert.Equal(t, "unsub-1", unsubAckMsg.ID)

	// Wait a bit
	time.Sleep(50 * time.Millisecond)

	// 6. Trigger another event
	docData2 := map[string]interface{}{
		"msg": "should not receive",
	}
	body2, _ := json.Marshal(map[string]interface{}{"data": docData2})
	req2, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/databases/default/documents/%s", env.APIURL, collectionName), bytes.NewBuffer(body2))
	require.NoError(t, err)
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req2)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// 7. Verify NO event received
	ws.SetReadDeadline(time.Now().Add(1 * time.Second))
	err = ws.ReadJSON(&eventMsg)
	assert.Error(t, err, "Should timeout and not receive event")
}

func TestRealtime_SSE(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	token := env.GetToken(t, "realtime-sse-user", "user")

	collectionName := "sse_test_col"
	sseURL := fmt.Sprintf("%s/realtime/sse?database=default&collection=%s", env.RealtimeURL, collectionName)

	req, err := http.NewRequest("GET", sseURL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", env.RealtimeURL)

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Verify Headers
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

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

	// 1. Read Initial Connection Message
	// Expect ": connected\n\n"
	line := readLine()
	assert.Equal(t, ": connected\n", line)
	line = readLine()
	assert.Equal(t, "\n", line)

	// Give some time for subscription to register
	time.Sleep(50 * time.Millisecond)

	// 2. Trigger Event
	docData := map[string]interface{}{
		"msg": "hello sse",
	}
	body, _ := json.Marshal(docData)
	postReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/databases/default/documents/%s", env.APIURL, collectionName), bytes.NewBuffer(body))
	require.NoError(t, err)
	postReq.Header.Set("Authorization", "Bearer "+token)
	postReq.Header.Set("Content-Type", "application/json")

	apiResp, err := http.DefaultClient.Do(postReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, apiResp.StatusCode)
	apiResp.Body.Close()

	// 3. Receive Event
	done := make(chan bool)

	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				dataStr := strings.TrimPrefix(line, "data: ")
				dataStr = strings.TrimSpace(dataStr)

				var msg BaseMessage
				err = json.Unmarshal([]byte(dataStr), &msg)
				if err == nil && msg.Type == TypeEvent {
					var eventPayload EventPayload
					if err := json.Unmarshal(msg.Payload, &eventPayload); err == nil {
						if val, ok := eventPayload.Delta.Document["msg"]; ok && val == "hello sse" {
							done <- true
							return
						}
					}
				}
			}
		}
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for SSE event")
	}
}

func TestRealtime_Stream(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	token := env.GetToken(t, "realtime-stream-user", "user")

	collectionName := "stream_test_col"
	wsURL := "ws" + strings.TrimPrefix(env.RealtimeURL, "http") + "/realtime/ws"

	// 1. Create a document beforehand
	docData := map[string]interface{}{
		"msg": "existing doc",
	}
	body, _ := json.Marshal(docData)
	createReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/databases/default/documents/%s", env.APIURL, collectionName), bytes.NewBuffer(body))
	require.NoError(t, err)
	createReq.Header.Set("Authorization", "Bearer "+token)
	createReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Connect to Websocket
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "Failed to connect to websocket")
	defer ws.Close()

	authMsg := BaseMessage{
		ID:   "auth-stream",
		Type: TypeAuth,
		Payload: mustMarshal(AuthPayload{
			Token:    token,
			Database: "default",
		}),
	}
	require.NoError(t, ws.WriteJSON(authMsg))

	var authAck BaseMessage
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	require.NoError(t, ws.ReadJSON(&authAck))
	assert.Equal(t, TypeAuthAck, authAck.Type)

	// 2. Send Subscribe Request with Snapshot
	streamID := "stream-1"
	subPayload := SubscribePayload{
		Query:        Query{Collection: collectionName},
		IncludeData:  true,
		SendSnapshot: true,
	}
	subMsg := BaseMessage{
		ID:      streamID,
		Type:    TypeSubscribe,
		Payload: mustMarshal(subPayload),
	}
	err = ws.WriteJSON(subMsg)
	require.NoError(t, err)

	// 3. Expect Snapshot with existing document
	var receivedSnapshot bool
	for i := 0; i < 5; i++ {
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		var msg BaseMessage
		err := ws.ReadJSON(&msg)
		require.NoError(t, err)

		if msg.Type == TypeSnapshot && msg.ID == streamID {
			var payload SnapshotPayload
			err := json.Unmarshal(msg.Payload, &payload)
			require.NoError(t, err)

			if len(payload.Documents) > 0 && payload.Documents[0]["msg"] == "existing doc" {
				receivedSnapshot = true
				break
			}
		}
	}
	assert.True(t, receivedSnapshot, "Should receive Snapshot with existing document")

	// 4. Trigger new event
	docData2 := map[string]interface{}{
		"msg": "new doc",
	}
	body2, _ := json.Marshal(docData2)
	createReq2, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/databases/default/documents/%s", env.APIURL, collectionName), bytes.NewBuffer(body2))
	require.NoError(t, err)
	createReq2.Header.Set("Authorization", "Bearer "+token)
	createReq2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(createReq2)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp2.StatusCode)
	resp2.Body.Close()

	// 5. Expect Event
	var receivedEvent bool
	for i := 0; i < 5; i++ {
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		var msg BaseMessage
		err := ws.ReadJSON(&msg)
		require.NoError(t, err)

		if msg.Type == TypeEvent {
			var payload EventPayload
			err := json.Unmarshal(msg.Payload, &payload)
			require.NoError(t, err)

			if payload.SubID == streamID && payload.Delta.Document["msg"] == "new doc" {
				receivedEvent = true
				break
			}
		}
	}
	assert.True(t, receivedEvent, "Should receive Event for new document")
}

func TestRealtime_Filtering(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	token := env.GetToken(t, "realtime-filter-user", "user")

	// Convert http URL to ws URL
	wsURL := "ws" + strings.TrimPrefix(env.RealtimeURL, "http") + "/realtime/ws"

	// Connect to Websocket
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "Failed to connect to websocket")
	defer ws.Close()

	// 1. Authenticate
	authMsg := BaseMessage{
		ID:      "auth-1",
		Type:    TypeAuth,
		Payload: mustMarshal(AuthPayload{Token: token, Database: "default"}),
	}
	err = ws.WriteJSON(authMsg)
	require.NoError(t, err)

	// Read Auth Ack
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ackMsg BaseMessage
	err = ws.ReadJSON(&ackMsg)
	require.NoError(t, err)
	assert.Equal(t, TypeAuthAck, ackMsg.Type)

	// 2. Subscribe with Filter (age > 20)
	collectionName := "realtime_filter_test"
	subID := "sub-filter"
	subMsg := BaseMessage{
		ID:   subID,
		Type: TypeSubscribe,
		Payload: mustMarshal(SubscribePayload{
			Query: Query{
				Collection: collectionName,
				Filters: []Filter{
					{Field: "age", Op: model.OpGt, Value: 20},
				},
			},
			IncludeData: true,
		}),
	}
	err = ws.WriteJSON(subMsg)
	require.NoError(t, err)

	// Read Subscribe Ack
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var subAckMsg BaseMessage
	err = ws.ReadJSON(&subAckMsg)
	require.NoError(t, err)
	assert.Equal(t, TypeSubscribeAck, subAckMsg.Type)

	// 3. Create Non-Matching Document (age = 18)
	docNoMatch := map[string]interface{}{
		"name": "Young Bob",
		"age":  18,
	}
	bodyNoMatch, _ := json.Marshal(docNoMatch)
	createReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/databases/default/documents/%s", env.APIURL, collectionName), bytes.NewBuffer(bodyNoMatch))
	require.NoError(t, err)
	createReq.Header.Set("Authorization", "Bearer "+token)
	createReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Verify NO event received (wait a bit)
	ws.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var unexpectedMsg BaseMessage
	err = ws.ReadJSON(&unexpectedMsg)
	if err == nil {
		// If we received a message, check if it's an event for our subscription
		if unexpectedMsg.Type == TypeEvent {
			var payload EventPayload
			json.Unmarshal(unexpectedMsg.Payload, &payload)
			if payload.SubID == subID {
				t.Fatalf("Received event that should have been filtered out: %+v", payload)
			}
		}
	} else {
		// Expected timeout or error
		assert.Contains(t, err.Error(), "i/o timeout")
	}

	// Reconnect to ensure clean state after timeout
	ws.Close()
	ws, _, err = websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer ws.Close()

	// Auth again
	authMsg2 := BaseMessage{
		Type:    TypeAuth,
		ID:      "auth-2",
		Payload: mustMarshal(AuthPayload{Token: token, Database: "default"}),
	}
	err = ws.WriteJSON(authMsg2)
	require.NoError(t, err)
	// Read auth ack
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	ws.ReadJSON(&BaseMessage{})

	// Subscribe again
	subMsg2 := BaseMessage{
		ID:   "sub-filter-2",
		Type: TypeSubscribe,
		Payload: mustMarshal(SubscribePayload{
			Query: Query{
				Collection: collectionName,
				Filters: []Filter{
					{Field: "age", Op: model.OpGt, Value: 20},
				},
			},
			IncludeData: true,
		}),
	}
	err = ws.WriteJSON(subMsg2)
	require.NoError(t, err)
	// Read sub ack
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	ws.ReadJSON(&BaseMessage{})

	// 4. Create Matching Document (age = 25)
	docMatch := map[string]interface{}{
		"name": "Adult Alice",
		"age":  25,
	}
	bodyMatch, _ := json.Marshal(docMatch)
	createReq2, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/databases/default/documents/%s", env.APIURL, collectionName), bytes.NewBuffer(bodyMatch))
	require.NoError(t, err)
	createReq2.Header.Set("Authorization", "Bearer "+token)
	createReq2.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(createReq2)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Verify Event Received
	fmt.Println("Test: Waiting for matching event...")
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var eventMsg BaseMessage
	err = ws.ReadJSON(&eventMsg)
	require.NoError(t, err, "Should receive matching event")
	assert.Equal(t, TypeEvent, eventMsg.Type)

	var eventPayload EventPayload
	err = json.Unmarshal(eventMsg.Payload, &eventPayload)
	require.NoError(t, err)
	assert.Equal(t, "sub-filter-2", eventPayload.SubID)
	assert.Equal(t, "Adult Alice", eventPayload.Delta.Document["name"])
}
