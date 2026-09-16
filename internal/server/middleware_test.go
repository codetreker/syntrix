package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponseDeadlineThroughMiddleware(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)
	handler := srv.wrapMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Error(err)
			http.Error(w, "deadline unavailable", http.StatusInternalServerError)
			return
		}
		select {
		case <-time.After(150 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, err := io.WriteString(w, "completed")
		assert.NoError(t, err)
	}))
	httpServer := httptest.NewUnstartedServer(handler)
	httpServer.Config.WriteTimeout = 50 * time.Millisecond
	httpServer.Start()
	defer httpServer.Close()
	client := httpServer.Client()
	client.Timeout = 3 * time.Second
	response, err := client.Get(httpServer.URL)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "completed", string(data))
}

func TestRequestIDMiddleware(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := GetRequestID(r.Context())
		assert.NotEmpty(t, id)
		w.Header().Set("X-Test-Request-ID", id)
	})

	handler := srv.requestIDMiddleware(nextHandler)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.NotEmpty(t, resp.Header.Get("X-Request-ID"))
	assert.Equal(t, resp.Header.Get("X-Request-ID"), resp.Header.Get("X-Test-Request-ID"))
}

func TestRequestIDMiddleware_ExistingID(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)

	existingID := "existing-id"
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := GetRequestID(r.Context())
		assert.Equal(t, existingID, id)
	})

	handler := srv.requestIDMiddleware(nextHandler)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", existingID)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, existingID, resp.Header.Get("X-Request-ID"))
}

func TestRecoveryMiddleware(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("oops")
	})

	handler := srv.recoveryMiddleware(nextHandler)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	// Should not panic
	assert.NotPanics(t, func() {
		handler.ServeHTTP(w, req)
	})

	resp := w.Result()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestCORSMiddleware(t *testing.T) {
	cfg := Config{EnableCORS: true, AllowCredentials: true}
	cfg.ApplyDefaults() // Apply defaults to get AllowedMethods and AllowedHeaders
	srv := New(cfg, nil).(*serverImpl)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := srv.corsMiddleware(nextHandler)

	// Test Preflight with Origin header
	req := httptest.NewRequest("OPTIONS", "/", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "https://example.com", resp.Header.Get("Access-Control-Allow-Origin"))
	assert.Contains(t, resp.Header.Get("Access-Control-Allow-Methods"), "GET")
	assert.Equal(t, "true", resp.Header.Get("Access-Control-Allow-Credentials"))

	// Test Normal Request with Origin header
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://example.com")
	w = httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp = w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "https://example.com", resp.Header.Get("Access-Control-Allow-Origin"))

	// Test Request without Origin header - should not set CORS headers
	req = httptest.NewRequest("GET", "/", nil)
	w = httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp = w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestCORSMiddleware_AllowedOrigins(t *testing.T) {
	cfg := Config{
		EnableCORS:     true,
		AllowedOrigins: []string{"https://allowed.com", "https://also-allowed.com"},
	}
	srv := New(cfg, nil).(*serverImpl)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := srv.corsMiddleware(nextHandler)

	// Test allowed origin
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://allowed.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, "https://allowed.com", resp.Header.Get("Access-Control-Allow-Origin"))

	// Test disallowed origin - should not set CORS headers
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Origin", "https://evil.com")
	w = httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp = w.Result()
	assert.Empty(t, resp.Header.Get("Access-Control-Allow-Origin"))
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := srv.securityHeadersMiddleware(nextHandler)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	resp := w.Result()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))
	assert.Equal(t, "1; mode=block", resp.Header.Get("X-XSS-Protection"))
	assert.Equal(t, "strict-origin-when-cross-origin", resp.Header.Get("Referrer-Policy"))
	assert.Equal(t, "default-src 'self'", resp.Header.Get("Content-Security-Policy"))
}

func TestRateLimitMiddleware(t *testing.T) {
	cfg := Config{
		RateLimit: RateLimitConfig{
			Enabled:  true,
			Requests: 2,
			Window:   time.Minute,
		},
	}
	cfg.ApplyDefaults()
	srv := New(cfg, nil).(*serverImpl)
	defer func() {
		if srv.rateLimiter != nil {
			if stoppable, ok := srv.rateLimiter.(interface{ Stop() }); ok {
				stoppable.Stop()
			}
		}
	}()

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := srv.rateLimitMiddleware(nextHandler)

	// First 2 requests should pass
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "Request %d should pass", i+1)
	}

	// 3rd request should be rate limited
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "60", w.Header().Get("Retry-After"))

	// Verify error response body
	var errResp struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	err := json.NewDecoder(w.Body).Decode(&errResp)
	assert.NoError(t, err)
	assert.Equal(t, "RATE_LIMITED", errResp.Code)
	assert.Equal(t, "Too many requests", errResp.Message)
}

func TestWrapMiddleware(t *testing.T) {
	cfg := Config{EnableCORS: true}
	srv := New(cfg, nil).(*serverImpl)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := srv.wrapMiddleware(handler)
	require.NotNil(t, wrapped)

	// Verify it works
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
}

func TestTimeoutMiddleware(t *testing.T) {
	timeout := 10 * time.Millisecond
	handler := TimeoutMiddleware(timeout)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if deadline is set
		_, ok := r.Context().Deadline()
		assert.True(t, ok)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid input")

	resp := w.Result()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var errResp APIError
	err := json.NewDecoder(resp.Body).Decode(&errResp)
	assert.NoError(t, err)
	assert.Equal(t, "BAD_REQUEST", errResp.Code)
	assert.Equal(t, "Invalid input", errResp.Message)
}

type FaultyResponseWriter struct {
	http.ResponseWriter
}

func (f *FaultyResponseWriter) Write(b []byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestWriteError_WriteFailure(t *testing.T) {
	w := httptest.NewRecorder()
	fw := &FaultyResponseWriter{ResponseWriter: w}

	// Should not panic and cover the error path
	writeError(fw, http.StatusInternalServerError, "ERROR", "msg")
}

// mockHijacker implements http.Hijacker for testing
type mockHijacker struct {
	http.ResponseWriter
	conn net.Conn
	rw   *bufio.ReadWriter
}

func (m *mockHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return m.conn, m.rw, nil
}

// mockFlusher implements http.Flusher for testing
type mockFlusher struct {
	http.ResponseWriter
	flushed bool
}

func (m *mockFlusher) Flush() {
	m.flushed = true
}

func TestResponseWriter_Hijack_Supported(t *testing.T) {
	w := httptest.NewRecorder()
	conn, _ := net.Pipe()
	defer conn.Close()
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

	hijacker := &mockHijacker{
		ResponseWriter: w,
		conn:           conn,
		rw:             rw,
	}

	rw2 := &responseWriter{ResponseWriter: hijacker}

	c, bufrw, err := rw2.Hijack()
	assert.NoError(t, err)
	assert.Equal(t, conn, c)
	assert.Equal(t, rw, bufrw)
}

func TestResponseWriter_Hijack_NotSupported(t *testing.T) {
	w := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: w}

	_, _, err := rw.Hijack()
	assert.Error(t, err)
	assert.Equal(t, http.ErrNotSupported, err)
}

func TestResponseWriter_Flush_Supported(t *testing.T) {
	w := httptest.NewRecorder()
	flusher := &mockFlusher{ResponseWriter: w}
	rw := &responseWriter{ResponseWriter: flusher}

	rw.Flush()
	assert.True(t, flusher.flushed)
}

func TestResponseWriter_Flush_NotSupported(t *testing.T) {
	// Create a ResponseWriter that doesn't implement http.Flusher
	nonFlusher := &FaultyResponseWriter{ResponseWriter: httptest.NewRecorder()}
	rw := &responseWriter{ResponseWriter: nonFlusher}

	// Should not panic when Flusher is not supported
	assert.NotPanics(t, func() {
		rw.Flush()
	})
}

func TestLoggingMiddleware_499Status(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(499) // Client Closed Request
	})

	handler := srv.loggingMiddleware(nextHandler)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, 499, w.Code)
}

func TestLoggingMiddleware_ContextCanceled(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	handler := srv.loggingMiddleware(nextHandler)

	// Create a canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest("GET", "/test", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestLoggingMiddleware_RealError(t *testing.T) {
	srv := New(Config{}, nil).(*serverImpl)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	handler := srv.loggingMiddleware(nextHandler)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
