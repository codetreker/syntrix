package realtime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity"
	api_config "github.com/codetreker/syntrix/internal/gateway/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_HandleMessage_Unauthenticated(t *testing.T) {
	// Setup
	hub := NewTestHub()
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)
	defer cancel()

	client := &Client{
		hub:           hub,
		send:          make(chan BaseMessage, 10),
		authenticated: false, // Not authenticated
	}

	// Test Subscribe when not authenticated
	msg := BaseMessage{
		ID:   "1",
		Type: TypeSubscribe,
	}
	client.handleMessage(msg)

	// Expect error message
	select {
	case resp := <-client.send:
		assert.Equal(t, TypeError, resp.Type)
		assert.Equal(t, "1", resp.ID)
		var payload ErrorPayload
		err := json.Unmarshal(resp.Payload, &payload)
		require.NoError(t, err)
		assert.Equal(t, "unauthorized", payload.Code)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}

	// Test Unsubscribe when not authenticated
	msg = BaseMessage{
		ID:   "2",
		Type: TypeUnsubscribe,
	}
	client.handleMessage(msg)

	// Expect error message
	select {
	case resp := <-client.send:
		assert.Equal(t, TypeError, resp.Type)
		assert.Equal(t, "2", resp.ID)
		var payload ErrorPayload
		err := json.Unmarshal(resp.Payload, &payload)
		require.NoError(t, err)
		assert.Equal(t, "unauthorized", payload.Code)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestHasSystemRole_NilContext(t *testing.T) {
	assert.False(t, hasSystemRole(nil))
}

func TestHasSystemRole_NoSystemRole(t *testing.T) {
	ctx := context.WithValue(context.Background(), identity.ContextKeyRoles, []string{"user", "admin"})
	assert.False(t, hasSystemRole(ctx))
}

func TestCheckAllowedOrigin_EmptyStringInConfig(t *testing.T) {
	cfg := api_config.RealtimeConfig{
		AllowedOrigins: []string{"http://example.com", ""}, // Contains empty string
	}

	// Should match valid origin
	err := checkAllowedOrigin("http://example.com", "example.com", cfg, false)
	assert.NoError(t, err)

	// Should not match invalid origin
	err = checkAllowedOrigin("http://evil.com", "example.com", cfg, false)
	assert.Error(t, err)
}

func TestServeSSE_SendChannelClosed(t *testing.T) {
	// Setup
	hub := NewTestHub()
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)

	cfg := api_config.RealtimeConfig{}

	req := httptest.NewRequest("GET", "/sse", nil)
	// Add database to context using internal context key since SSE requires authentication
	reqCtx := context.WithValue(req.Context(), contextKeyDatabase, "test-database")
	req = req.WithContext(reqCtx)
	w := httptest.NewRecorder()

	// We need to run ServeSSE in a goroutine because it blocks
	done := make(chan bool)
	go func() {
		ServeSSE(hub, nil, nil, cfg, w, req)
		done <- true
	}()

	// Wait for connection to be established
	time.Sleep(10 * time.Millisecond)

	// Cancel the hub context. This triggers hub.shutdownClients(), which closes client.send channels.
	cancel()

	// Wait for ServeSSE to return
	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ServeSSE to return")
	}
}
