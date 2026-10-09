package ratelimit

import (
	"sync"
	"time"
)

// memoryLimiter implements an in-memory rate limiter using the Token Bucket algorithm.
// Token Bucket is more efficient than sliding window as it only needs to store
// a fixed amount of data per key (O(1) space and time per operation).
type memoryLimiter struct {
	mu      sync.RWMutex
	buckets map[string]*tokenBucket
	config  Config

	// For lazy cleanup of stale buckets
	cleanupT *time.Ticker
	stopCh   chan struct{}
	stopped  bool
}

// tokenBucket implements the Token Bucket algorithm.
// Instead of storing all request timestamps, it tracks:
// - tokens: current number of available tokens (float64 for precision)
// - lastUpdate: when tokens were last calculated
// Tokens are refilled at a constant rate (capacity/window).
type tokenBucket struct {
	tokens     float64   // Current token count
	lastUpdate time.Time // Last time tokens were updated
}

// NewMemoryLimiter creates a new in-memory rate limiter using Token Bucket algorithm.
func NewMemoryLimiter(cfg Config) Limiter {
	l := &memoryLimiter{
		buckets: make(map[string]*tokenBucket),
		config:  cfg,
		stopCh:  make(chan struct{}),
	}

	// Start cleanup goroutine to remove stale buckets
	// This is less critical now since each bucket is O(1) space,
	// but still good for memory hygiene with many unique keys
	l.cleanupT = time.NewTicker(cfg.Window * 2)
	go l.cleanup()

	return l
}

// Allow checks if a request from the given key should be allowed.
// Uses Token Bucket algorithm: tokens refill at rate = capacity/window.
func (l *memoryLimiter) Allow(key string) bool {
	if !l.config.Enabled {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	capacity := float64(l.config.Requests)
	fillRate := capacity / l.config.Window.Seconds() // tokens per second

	b, exists := l.buckets[key]
	if !exists {
		// Create new bucket with capacity-1 tokens (this request consumes 1)
		l.buckets[key] = &tokenBucket{
			tokens:     capacity - 1,
			lastUpdate: now,
		}
		return true
	}

	// Calculate tokens to add based on elapsed time
	elapsed := now.Sub(b.lastUpdate).Seconds()
	b.tokens = min(capacity, b.tokens+elapsed*fillRate)
	b.lastUpdate = now

	// Check if we have at least 1 token
	if b.tokens >= 1 {
		b.tokens--
		return true
	}

	return false
}

// Reset clears the rate limit counter for the given key.
func (l *memoryLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// cleanup periodically removes stale buckets to prevent memory leaks.
// A bucket is considered stale if it has been at full capacity for a while
// (meaning no recent requests).
func (l *memoryLimiter) cleanup() {
	for {
		select {
		case <-l.cleanupT.C:
			l.cleanupStale()
		case <-l.stopCh:
			l.cleanupT.Stop()
			return
		}
	}
}

// cleanupStale removes buckets that haven't been accessed recently.
// A bucket is stale if enough time has passed for it to be at full capacity.
func (l *memoryLimiter) cleanupStale() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	staleThreshold := l.config.Window * 2 // Remove after 2x window of inactivity

	for key, b := range l.buckets {
		if now.Sub(b.lastUpdate) > staleThreshold {
			delete(l.buckets, key)
		}
	}
}

// Stop stops the cleanup goroutine. Should be called when the limiter is no longer needed.
// Safe to call multiple times.
func (l *memoryLimiter) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	l.stopped = true
	close(l.stopCh)
}

// Stoppable extends Limiter with a Stop method for cleanup.
type Stoppable interface {
	Limiter
	Stop()
}

// Ensure memoryLimiter implements Stoppable.
var _ Stoppable = (*memoryLimiter)(nil)
