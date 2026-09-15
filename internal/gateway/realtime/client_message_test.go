package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	api_config "github.com/syntrixbase/syntrix/internal/gateway/config"
	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestClientHandleMessage_AuthAck(t *testing.T) {
	c := &Client{hub: NewTestHub(), queryService: &MockQueryService{}, send: make(chan BaseMessage, 1), subscriptions: make(map[string]Subscription), streamerSubIDs: make(map[string]string), auth: &mockAuthService{}}
	payload, _ := json.Marshal(AuthPayload{Token: "good", Database: "default"})
	c.handleMessage(BaseMessage{Type: TypeAuth, ID: "req", Payload: payload})

	select {
	case msg := <-c.send:
		assert.Equal(t, TypeAuthAck, msg.Type)
		assert.Equal(t, "req", msg.ID)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected auth ack")
	}
}

func TestClientHandleMessage_AuthError(t *testing.T) {
	c := &Client{
		hub:          NewTestHub(),
		queryService: &MockQueryService{},
		send:         make(chan BaseMessage, 1),
		auth:         &mockAuthService{},
	}

	// Case 1: Invalid Payload
	c.handleMessage(BaseMessage{Type: TypeAuth, ID: "req1", Payload: []byte(`invalid`)})
	select {
	case msg := <-c.send:
		assert.Equal(t, TypeError, msg.Type)
		assert.Equal(t, "req1", msg.ID)
		assert.Contains(t, string(msg.Payload), "invalid_auth")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected error")
	}

	// Case 2: Missing Database
	payload, _ := json.Marshal(AuthPayload{Token: "good"})
	c.handleMessage(BaseMessage{Type: TypeAuth, ID: "req2", Payload: payload})
	select {
	case msg := <-c.send:
		assert.Equal(t, TypeError, msg.Type)
		assert.Equal(t, "req2", msg.ID)
		assert.Contains(t, string(msg.Payload), "database is required")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected error")
	}

	// Case 3: Invalid Token
	payload, _ = json.Marshal(AuthPayload{Token: "bad", Database: "default"})
	c.handleMessage(BaseMessage{Type: TypeAuth, ID: "req3", Payload: payload})
	select {
	case msg := <-c.send:
		assert.Equal(t, TypeError, msg.Type)
		assert.Equal(t, "req3", msg.ID)
		assert.Contains(t, string(msg.Payload), "unauthorized")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected error")
	}
}

func TestClientHandleMessage_AuthSuccess(t *testing.T) {
	c := &Client{
		hub:          NewTestHub(),
		queryService: &MockQueryService{},
		send:         make(chan BaseMessage, 1),
		auth:         &mockAuthService{},
	}

	payload, _ := json.Marshal(AuthPayload{Token: "good", Database: "default"})
	c.handleMessage(BaseMessage{Type: TypeAuth, ID: "req-ok", Payload: payload})

	select {
	case msg := <-c.send:
		assert.Equal(t, TypeAuthAck, msg.Type)
		assert.Equal(t, "req-ok", msg.ID)
		assert.True(t, c.authenticated)
		assert.Equal(t, "default", c.database)
		assert.False(t, c.allowAllDatabases)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected auth ack")
	}
}

type mockAuthServiceSystem struct {
	mockAuthService
}

func (m *mockAuthServiceSystem) ValidateToken(tokenString string) (*identity.Claims, error) {
	if tokenString == "system" {
		// Database is now extracted from auth payload, not token
		return &identity.Claims{Roles: []string{"system"}}, nil
	}
	return m.mockAuthService.ValidateToken(tokenString)
}

func TestClientHandleMessage_AuthSystemRole(t *testing.T) {
	c := &Client{
		hub:          NewTestHub(),
		queryService: setupMockQuery(),
		send:         make(chan BaseMessage, 1),
		auth:         &mockAuthServiceSystem{},
	}

	payload, _ := json.Marshal(AuthPayload{Token: "system", Database: "default"})
	c.handleMessage(BaseMessage{Type: TypeAuth, ID: "req", Payload: payload})

	select {
	case msg := <-c.send:
		assert.Equal(t, TypeAuthAck, msg.Type)
		assert.True(t, c.authenticated)
		assert.True(t, c.allowAllDatabases)
		assert.Equal(t, "default", c.database)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected auth ack")
	}
}

func TestClientHandleMessage_SubscribeSnapshot(t *testing.T) {
	queryService := setupMockQuery()
	c := &Client{hub: NewTestHub(), queryService: queryService, send: make(chan BaseMessage, 2), subscriptions: make(map[string]Subscription), streamerSubIDs: make(map[string]string), authenticated: true}
	payload := SubscribePayload{Query: model.Query{Collection: "users"}, IncludeData: true, SendSnapshot: true}
	b, _ := json.Marshal(payload)

	c.handleMessage(BaseMessage{Type: TypeSubscribe, ID: "sub", Payload: b})

	// Expect SubscribeAck then Snapshot
	var msg1, msg2 BaseMessage
	select {
	case msg1 = <-c.send:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for subscribe ack")
	}
	select {
	case msg2 = <-c.send:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for snapshot")
	}
	assert.Equal(t, TypeSubscribeAck, msg1.Type)
	assert.Equal(t, TypeSnapshot, msg2.Type)
	assert.JSONEq(t, `{"subId":"sub","documents":[{"id":"1","name":"test","collection":"users","version":1,"createdAt":100,"updatedAt":200}]}`, string(msg2.Payload))
	queryService.AssertCalled(t, "Pull", mock.MatchedBy(func(ctx context.Context) bool {
		deadline, ok := ctx.Deadline()
		return ok && time.Until(deadline) > 0 && time.Until(deadline) <= 10*time.Second
	}), "", storage.ReplicationPullRequest{Collection: "users", Checkpoint: "", Limit: 1000})
}

func TestClientHandleMessage_Unsubscribe(t *testing.T) {
	c := &Client{hub: NewTestHub(), queryService: setupMockQuery(), send: make(chan BaseMessage, 1), subscriptions: map[string]Subscription{"sub": {}}, authenticated: true}
	payload := UnsubscribePayload{ID: "sub"}
	b, _ := json.Marshal(payload)

	c.handleMessage(BaseMessage{Type: TypeUnsubscribe, ID: "req1", Payload: b})

	_, ok := c.subscriptions["sub"]
	assert.False(t, ok)

	select {
	case msg := <-c.send:
		assert.Equal(t, TypeUnsubscribeAck, msg.Type)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected unsubscribe ack")
	}
}

func TestReadPump_InvalidJSONContinues(t *testing.T) {
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()

	hub := NewTestHub()
	go hub.Run(hubCtx)
	qs := setupMockQuery()
	auth := &mockAuthService{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(hub, qs, auth, api_config.RealtimeConfig{}, w, r)
	}))
	defer server.Close()

	u := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	assert.NoError(t, err)
	defer conn.Close()

	// Send invalid JSON to trigger unmarshal error path; expect no panic and ability to continue
	assert.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("{invalid")))

	// Follow with valid auth to ensure readPump still processes
	payload, _ := json.Marshal(AuthPayload{Token: "good", Database: "default"})
	authMsg := BaseMessage{Type: TypeAuth, ID: "auth-2", Payload: payload}
	assert.NoError(t, conn.WriteJSON(authMsg))

	var resp BaseMessage
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	assert.NoError(t, conn.ReadJSON(&resp))
	assert.Equal(t, TypeAuthAck, resp.Type)
}

func TestServeWs_RejectsCrossOrigin(t *testing.T) {
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()

	hub := NewTestHub()
	go hub.Run(hubCtx)
	qs := setupMockQuery()
	auth := &mockAuthService{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(hub, qs, auth, api_config.RealtimeConfig{}, w, r)
	}))
	defer server.Close()

	// Origin does not match server host
	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Origin", "http://evil.example")
	_, _, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
	assert.Error(t, err)
}

func TestHandleMessage_SubscribeCompileError(t *testing.T) {
	hub := NewHub()
	ms := new(MockStreamerStream)
	ms.On("Subscribe", mock.Anything, mock.Anything, mock.Anything).Return("", errors.New("compile error"))
	ms.On("Unsubscribe", mock.Anything).Return(nil).Maybe()
	hub.SetStream(ms)

	c := &Client{hub: hub, queryService: setupMockQuery(), send: make(chan BaseMessage, 1), subscriptions: make(map[string]Subscription), streamerSubIDs: make(map[string]string), authenticated: true}
	payload := SubscribePayload{Query: model.Query{Filters: []model.Filter{{Field: "age", Op: "!", Value: 1}}}}
	b, _ := json.Marshal(payload)

	c.handleMessage(BaseMessage{Type: TypeSubscribe, ID: "sub-err", Payload: b})

	select {
	case msg := <-c.send:
		assert.Equal(t, TypeError, msg.Type)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for error message")
	}
}

func TestHandleMessage_SubscribeBadJSON(t *testing.T) {
	c := &Client{hub: NewTestHub(), queryService: setupMockQuery(), send: make(chan BaseMessage, 1), subscriptions: make(map[string]Subscription), streamerSubIDs: make(map[string]string), authenticated: true}

	c.handleMessage(BaseMessage{Type: TypeSubscribe, ID: "sub-bad", Payload: []byte("{bad")})

	select {
	case <-c.send:
		t.Fatal("should not send when payload invalid")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestWritePump_StopsOnChannelClose(t *testing.T) {
	clientCh := make(chan *Client, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := &Client{conn: conn, send: make(chan BaseMessage, 1)}
		clientCh <- c
		go c.writePump()
	}))
	defer server.Close()

	u := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	assert.NoError(t, err)
	defer conn.Close()

	var c *Client
	select {
	case c = <-clientCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for client connection")
	}

	// Send one message through writePump
	c.send <- BaseMessage{Type: TypeAuthAck, ID: "x"}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	msgType, data, err := conn.ReadMessage()
	assert.NoError(t, err)
	assert.Equal(t, websocket.TextMessage, msgType)
	assert.Contains(t, string(data), "auth_ack")

	// Closing channel should make writePump emit a close frame and exit
	close(c.send)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err = conn.ReadMessage()
	assert.Error(t, err)
}

func TestWritePump_SendsPing(t *testing.T) {
	original := pingPeriod
	pingPeriod = 10 * time.Millisecond
	defer func() { pingPeriod = original }()

	_ = NewTestHub()
	clientCh := make(chan *Client, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := &Client{conn: conn, send: make(chan BaseMessage, 1)}
		clientCh <- c
		go c.writePump()
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	assert.NoError(t, err)
	defer conn.Close()

	pings := make(chan struct{}, 1)
	conn.SetPingHandler(func(appData string) error {
		pings <- struct{}{}
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// drive control frame processing
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}
	}()

	// Wait for ping
	select {
	case <-pings:
	case <-time.After(800 * time.Millisecond):
		t.Fatal("expected ping from writePump")
	}

	// Cleanup writePump goroutine
	var c *Client
	select {
	case c = <-clientCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for client connection")
	}
	close(c.send)
}

func TestWritePump_SendsHeartbeat(t *testing.T) {
	// Set heartbeat interval to a short duration for testing
	originalHeartbeat := heartbeatInterval
	heartbeatInterval = 10 * time.Millisecond
	defer func() { heartbeatInterval = originalHeartbeat }()

	_ = NewTestHub()
	clientCh := make(chan *Client, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := &Client{conn: conn, send: make(chan BaseMessage, 1)}
		clientCh <- c
		go c.writePump()
	}))
	defer server.Close()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	assert.NoError(t, err)
	defer conn.Close()

	// Read messages until we get a heartbeat
	heartbeatReceived := false
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	for i := 0; i < 10; i++ {
		var msg BaseMessage
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		if msg.Type == TypeHeartbeat {
			heartbeatReceived = true
			break
		}
	}

	assert.True(t, heartbeatReceived, "expected heartbeat message from writePump")

	// Cleanup writePump goroutine
	var c *Client
	select {
	case c = <-clientCh:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for client connection")
	}
	close(c.send)
}

func TestServeWs_ReadWriteCycle(t *testing.T) {
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()

	hub := NewTestHub()
	go hub.Run(hubCtx)

	qs := setupMockQuery()
	auth := &mockAuthService{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(hub, qs, auth, api_config.RealtimeConfig{}, w, r)
	}))
	defer server.Close()

	u := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	assert.NoError(t, err)
	defer conn.Close()

	// Auth message -> expect ack
	payload, _ := json.Marshal(AuthPayload{Token: "good", Database: "default"})
	authMsg := BaseMessage{Type: TypeAuth, ID: "auth-1", Payload: payload}
	assert.NoError(t, conn.WriteJSON(authMsg))

	var resp BaseMessage
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	assert.NoError(t, conn.ReadJSON(&resp))
	assert.Equal(t, TypeAuthAck, resp.Type)
	assert.Equal(t, "auth-1", resp.ID)

	// Subscribe with snapshot -> expect ack then snapshot
	subPayload := SubscribePayload{Query: model.Query{Collection: "users"}, IncludeData: true, SendSnapshot: true}
	body, _ := json.Marshal(subPayload)
	assert.NoError(t, conn.WriteJSON(BaseMessage{Type: TypeSubscribe, ID: "sub-1", Payload: body}))

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	assert.NoError(t, conn.ReadJSON(&resp))
	assert.Equal(t, TypeSubscribeAck, resp.Type)

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	assert.NoError(t, conn.ReadJSON(&resp))
	assert.Equal(t, TypeSnapshot, resp.Type)
}

func setupMockQuery() *MockQueryService {
	m := new(MockQueryService)
	// Mock Pull for Snapshot
	m.On("Pull", mock.Anything, mock.Anything, mock.Anything).Return(&storage.ReplicationPullResponse{
		Documents:  []model.Document{{"id": "1", "name": "test", "collection": "users", "version": int64(1), "createdAt": int64(100), "updatedAt": int64(200)}},
		Checkpoint: "caught-up",
		CaughtUp:   true,
	}, nil).Maybe()
	return m
}
