package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api_config "github.com/codetreker/syntrix/internal/gateway/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestTokenFromQueryParam_TableDriven(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{"Nil request", "", ""},
		{"access_token", "/?access_token=abc", "abc"},
		{"token", "/?token=xyz", "xyz"},
		{"Both (access_token takes precedence)", "/?access_token=abc&token=xyz", "abc"},
		{"None", "/", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var req *http.Request
			if tt.url != "" {
				req = httptest.NewRequest("GET", tt.url, nil)
			}
			assert.Equal(t, tt.expected, tokenFromQueryParam(req))
		})
	}
}

func TestServer_HandleWS_TableDriven(t *testing.T) {
	t.Parallel()
	mockQS := new(MockQueryService)
	mockStreamer := new(MockStreamerService)
	server := NewServer(mockQS, mockStreamer, "docs", &mockAuthService{}, api_config.RealtimeConfig{})

	// Start Hub
	ctxHub, cancelHub := context.WithCancel(context.Background())
	t.Cleanup(cancelHub)
	go server.hub.Run(ctxHub)

	tests := []struct {
		name           string
		url            string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "Invalid WS Upgrade",
			url:            "/ws",
			expectedStatus: http.StatusBadRequest, // Upgrader usually returns 400
		},
		{
			name:           "Query Token Not Allowed",
			url:            "/ws?access_token=bad",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   "Query token not allowed",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("GET", tt.url, nil)
			w := httptest.NewRecorder()

			server.HandleWS(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.expectedBody != "" {
				assert.Contains(t, w.Body.String(), tt.expectedBody)
			}
		})
	}
}

func TestServer_HandleSSE_TableDriven(t *testing.T) {
	t.Parallel()
	mockQS := new(MockQueryService)
	mockStreamer := new(MockStreamerService)
	server := NewServer(mockQS, mockStreamer, "docs", &mockAuthService{}, api_config.RealtimeConfig{
		AllowedOrigins: []string{"http://example.com"}})

	// Setup Hub Stream
	ms := new(MockStreamerStream)
	ms.On("Subscribe", mock.Anything, mock.Anything, mock.Anything).Return("sub-id", nil).Maybe()
	ms.On("Unsubscribe", mock.Anything).Return(nil).Maybe()
	server.hub.SetStream(ms)

	// Start Hub
	ctxHub, cancelHub := context.WithCancel(context.Background())
	t.Cleanup(cancelHub)
	go server.hub.Run(ctxHub)

	tests := []struct {
		name           string
		headers        map[string]string
		expectedStatus int
		expectedBody   string
	}{
		{
			name: "Success",
			headers: map[string]string{
				"Authorization": "Bearer good",
				"Origin":        "http://example.com",
			},
			expectedStatus: http.StatusOK,
		},
		// Add more scenarios if needed, e.g., missing auth, invalid origin
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			// SSE requires database in context
			ctx = context.WithValue(ctx, contextKeyDatabase, "default")
			req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()

			// Cancel context shortly to unblock HandleSSE
			go func() {
				time.Sleep(5 * time.Millisecond)
				cancel()
			}()

			server.HandleSSE(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
		})
	}
}
