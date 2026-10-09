package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMemoryLimiter(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 10,
		Window:   time.Minute,
	}

	limiter := NewMemoryLimiter(cfg)
	require.NotNil(t, limiter)

	// Clean up
	if s, ok := limiter.(Stoppable); ok {
		s.Stop()
	}
}

func TestMemoryLimiter_Allow(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 3,
		Window:   time.Minute,
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	key := "test-key"

	// First 3 requests should be allowed
	assert.True(t, limiter.Allow(key), "Request 1 should be allowed")
	assert.True(t, limiter.Allow(key), "Request 2 should be allowed")
	assert.True(t, limiter.Allow(key), "Request 3 should be allowed")

	// 4th request should be denied
	assert.False(t, limiter.Allow(key), "Request 4 should be denied")
	assert.False(t, limiter.Allow(key), "Request 5 should be denied")
}

func TestMemoryLimiter_Allow_DifferentKeys(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 2,
		Window:   time.Minute,
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	// Different keys should have independent limits
	assert.True(t, limiter.Allow("key1"))
	assert.True(t, limiter.Allow("key1"))
	assert.False(t, limiter.Allow("key1"))

	// key2 should still have its full quota
	assert.True(t, limiter.Allow("key2"))
	assert.True(t, limiter.Allow("key2"))
	assert.False(t, limiter.Allow("key2"))
}

func TestMemoryLimiter_Allow_Disabled(t *testing.T) {
	cfg := Config{
		Enabled:  false,
		Requests: 1,
		Window:   time.Minute,
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	// All requests should be allowed when disabled
	assert.True(t, limiter.Allow("key"))
	assert.True(t, limiter.Allow("key"))
	assert.True(t, limiter.Allow("key"))
}

func TestMemoryLimiter_Reset(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 2,
		Window:   time.Minute,
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	key := "test-key"

	// Use up the quota
	assert.True(t, limiter.Allow(key))
	assert.True(t, limiter.Allow(key))
	assert.False(t, limiter.Allow(key))

	// Reset the key
	limiter.Reset(key)

	// Should be allowed again
	assert.True(t, limiter.Allow(key))
	assert.True(t, limiter.Allow(key))
	assert.False(t, limiter.Allow(key))
}

func TestMemoryLimiter_WindowExpiry(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 2,
		Window:   50 * time.Millisecond,
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	key := "test-key"

	// Use up the quota
	assert.True(t, limiter.Allow(key))
	assert.True(t, limiter.Allow(key))
	assert.False(t, limiter.Allow(key))

	// Wait for window to expire
	time.Sleep(60 * time.Millisecond)

	// Should be allowed again
	assert.True(t, limiter.Allow(key), "Should be allowed after window expiry")
}

func TestMemoryLimiter_Concurrent(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 100,
		Window:   time.Minute,
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	var wg sync.WaitGroup
	allowed := make(chan bool, 200)

	// Launch 200 concurrent requests
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed <- limiter.Allow("concurrent-key")
		}()
	}

	wg.Wait()
	close(allowed)

	// Count allowed requests
	count := 0
	for a := range allowed {
		if a {
			count++
		}
	}

	// Exactly 100 should be allowed
	assert.Equal(t, 100, count, "Exactly 100 requests should be allowed")
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.True(t, cfg.Enabled)
	assert.Equal(t, 100, cfg.Requests)
	assert.Equal(t, time.Minute, cfg.Window)
}

func TestAuthConfig(t *testing.T) {
	cfg := AuthConfig()

	assert.True(t, cfg.Enabled)
	assert.Equal(t, 5, cfg.Requests)
	assert.Equal(t, time.Minute, cfg.Window)
}

func TestMiddleware(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 2,
		Window:   time.Minute,
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	middleware := Middleware(limiter)
	wrapped := middleware(handler)

	// First 2 requests should pass
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		w := httptest.NewRecorder()
		wrapped.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "Request %d should pass", i+1)
	}

	// 3rd request should be rate limited
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "60", w.Header().Get("Retry-After"))
}

// TestTokenBucket_GradualRefill verifies that the Token Bucket algorithm
// refills tokens gradually over time, not all at once when the window expires.
func TestTokenBucket_GradualRefill(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 10,                     // 10 tokens capacity
		Window:   100 * time.Millisecond, // refill rate = 100 tokens/sec
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	key := "gradual-refill-key"

	// Use all 10 tokens
	for i := 0; i < 10; i++ {
		assert.True(t, limiter.Allow(key), "Request %d should be allowed", i+1)
	}
	assert.False(t, limiter.Allow(key), "Request 11 should be denied")

	// Wait for half the window (should refill ~5 tokens)
	time.Sleep(50 * time.Millisecond)

	// Should be able to make ~5 more requests (Token Bucket gradual refill)
	allowedCount := 0
	for i := 0; i < 10; i++ {
		if limiter.Allow(key) {
			allowedCount++
		}
	}
	// Should allow approximately 5 requests (4-6 due to timing variance)
	assert.GreaterOrEqual(t, allowedCount, 4, "Should allow at least 4 requests after half window")
	assert.LessOrEqual(t, allowedCount, 6, "Should allow at most 6 requests after half window")
}

// TestTokenBucket_BurstHandling verifies burst handling capability.
func TestTokenBucket_BurstHandling(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 5,
		Window:   time.Second,
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	key := "burst-key"

	// Can burst up to capacity
	for i := 0; i < 5; i++ {
		assert.True(t, limiter.Allow(key), "Burst request %d should be allowed", i+1)
	}
	assert.False(t, limiter.Allow(key), "Request after burst should be denied")

	// Wait a bit for partial refill
	time.Sleep(200 * time.Millisecond) // Should refill ~1 token

	// Should allow 1 more request
	assert.True(t, limiter.Allow(key), "Should allow 1 request after partial refill")
	assert.False(t, limiter.Allow(key), "Should deny subsequent request")
}

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		expected   string
	}{
		{
			name:       "RemoteAddr only",
			remoteAddr: "192.168.1.1:12345",
			expected:   "192.168.1.1",
		},
		{
			name:       "X-Forwarded-For single",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.195"},
			expected:   "203.0.113.195",
		},
		{
			name:       "X-Forwarded-For multiple",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.195, 70.41.3.18, 150.172.238.178"},
			expected:   "203.0.113.195",
		},
		{
			name:       "X-Real-IP",
			remoteAddr: "10.0.0.1:12345",
			headers:    map[string]string{"X-Real-IP": "203.0.113.195"},
			expected:   "203.0.113.195",
		},
		{
			name:       "X-Forwarded-For takes precedence over X-Real-IP",
			remoteAddr: "10.0.0.1:12345",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.195",
				"X-Real-IP":       "70.41.3.18",
			},
			expected: "203.0.113.195",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			ip := GetClientIP(req)
			assert.Equal(t, tt.expected, ip)
		})
	}
}

// TestMemoryLimiter_CleanupStale verifies that stale buckets are cleaned up.
func TestMemoryLimiter_CleanupStale(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 5,
		Window:   20 * time.Millisecond, // Very short window for testing
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	// Cast to access internal state for testing
	ml := limiter.(*memoryLimiter)

	// Make a request to create a bucket
	assert.True(t, limiter.Allow("stale-key"))

	// Verify bucket exists
	ml.mu.RLock()
	_, exists := ml.buckets["stale-key"]
	ml.mu.RUnlock()
	assert.True(t, exists, "Bucket should exist after request")

	// Wait for stale threshold (2x window = 40ms)
	time.Sleep(50 * time.Millisecond)

	// Manually trigger cleanup
	ml.cleanupStale()

	// Verify bucket was removed
	ml.mu.RLock()
	_, exists = ml.buckets["stale-key"]
	ml.mu.RUnlock()
	assert.False(t, exists, "Stale bucket should be removed after cleanup")
}

// TestMemoryLimiter_CleanupStale_KeepsActive verifies active buckets are not cleaned.
func TestMemoryLimiter_CleanupStale_KeepsActive(t *testing.T) {
	cfg := Config{
		Enabled:  true,
		Requests: 5,
		Window:   100 * time.Millisecond,
	}

	limiter := NewMemoryLimiter(cfg)
	defer func() {
		if s, ok := limiter.(Stoppable); ok {
			s.Stop()
		}
	}()

	ml := limiter.(*memoryLimiter)

	// Make requests to create buckets
	assert.True(t, limiter.Allow("active-key"))
	assert.True(t, limiter.Allow("stale-key"))

	// Wait a bit, then refresh only active-key
	time.Sleep(50 * time.Millisecond)
	assert.True(t, limiter.Allow("active-key")) // Refresh active-key

	// Wait for stale-key to become stale (need 2x window = 200ms total)
	time.Sleep(160 * time.Millisecond)

	// Trigger cleanup
	ml.cleanupStale()

	// active-key should still exist (was recently accessed)
	ml.mu.RLock()
	_, activeExists := ml.buckets["active-key"]
	_, staleExists := ml.buckets["stale-key"]
	ml.mu.RUnlock()

	assert.True(t, activeExists, "Active bucket should still exist")
	assert.False(t, staleExists, "Stale bucket should be removed")
}
