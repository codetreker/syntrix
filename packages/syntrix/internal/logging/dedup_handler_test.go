// internal/logging/dedup_handler_test.go
package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDedupHandler_BasicDedup(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewTextHandler(&buf, nil)

	cfg := DedupHandlerConfig{
		BatchSize:    100,
		FlushTimeout: 50 * time.Millisecond,
	}
	dh := NewDedupHandlerWithConfig(baseHandler, cfg)

	logger := slog.New(dh)

	// Log identical messages
	for i := 0; i < 5; i++ {
		logger.Info("duplicate message", "key", "value")
	}

	// Close flushes pending messages and stops background goroutine
	err := dh.Close()
	require.NoError(t, err)

	content := buf.String()

	// Should have only one message with repeated_count
	assert.Equal(t, 1, strings.Count(content, "duplicate message"), "Should deduplicate identical messages")
	assert.Contains(t, content, "repeated_count=5", "Should have repeated_count attribute")
}

func TestDedupHandler_DifferentMessages(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewTextHandler(&buf, nil)

	cfg := DedupHandlerConfig{
		BatchSize:    100,
		FlushTimeout: 50 * time.Millisecond,
	}
	dh := NewDedupHandlerWithConfig(baseHandler, cfg)

	logger := slog.New(dh)

	// Log different messages
	logger.Info("message 1")
	logger.Info("message 2")
	logger.Info("message 3")

	// Close flushes pending messages and stops background goroutine
	err := dh.Close()
	require.NoError(t, err)

	content := buf.String()

	// Should have all 3 messages
	assert.Equal(t, 1, strings.Count(content, "message 1"))
	assert.Equal(t, 1, strings.Count(content, "message 2"))
	assert.Equal(t, 1, strings.Count(content, "message 3"))
	assert.NotContains(t, content, "repeated_count", "Should not have repeated_count for unique messages")
}

func TestDedupHandler_WithAttrsDistinguishes(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewTextHandler(&buf, nil)

	cfg := DedupHandlerConfig{
		BatchSize:    100,
		FlushTimeout: 50 * time.Millisecond,
	}
	dh := NewDedupHandlerWithConfig(baseHandler, cfg)

	// Create two loggers with different attrs
	logger1 := slog.New(dh).With("component", "server")
	logger2 := slog.New(dh).With("component", "client")

	// Log same message from different components
	logger1.Info("connection established")
	logger2.Info("connection established")

	// Close flushes pending messages and stops background goroutine
	err := dh.Close()
	require.NoError(t, err)

	content := buf.String()

	// Should have 2 messages (not deduplicated because different attrs)
	assert.Equal(t, 2, strings.Count(content, "connection established"),
		"Messages with different preset attrs should not be deduplicated")
}

func TestDedupHandler_WithGroupDistinguishes(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewTextHandler(&buf, nil)

	cfg := DedupHandlerConfig{
		BatchSize:    100,
		FlushTimeout: 50 * time.Millisecond,
	}
	dh := NewDedupHandlerWithConfig(baseHandler, cfg)

	// Create two loggers with different groups
	logger1 := slog.New(dh).WithGroup("http")
	logger2 := slog.New(dh).WithGroup("grpc")

	// Log same message from different groups
	logger1.Info("request received")
	logger2.Info("request received")

	// Close flushes pending messages and stops background goroutine
	err := dh.Close()
	require.NoError(t, err)

	content := buf.String()

	// Should have 2 messages (not deduplicated because different groups)
	assert.Equal(t, 2, strings.Count(content, "request received"),
		"Messages with different groups should not be deduplicated")
}

func TestDedupHandler_ConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex

	// Thread-safe writer
	safeWriter := &syncWriter{w: &buf, mu: &mu}
	baseHandler := slog.NewTextHandler(safeWriter, nil)

	cfg := DedupHandlerConfig{
		BatchSize:    1000,
		FlushTimeout: 100 * time.Millisecond,
	}
	dh := NewDedupHandlerWithConfig(baseHandler, cfg)
	defer dh.Close()

	logger := slog.New(dh)

	// Concurrent writes
	var wg sync.WaitGroup
	numGoroutines := 10
	messagesPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerGoroutine; j++ {
				// All goroutines log the same message
				logger.Info("concurrent message", "id", id)
			}
		}(i)
	}

	wg.Wait()

	// Wait for flush
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	content := buf.String()
	mu.Unlock()

	// Should have 10 unique messages (one per goroutine id)
	// Each should have repeated_count=100
	count := strings.Count(content, "concurrent message")
	assert.Equal(t, numGoroutines, count, "Should have one message per unique id")

	// Verify repeated_count is present
	assert.Contains(t, content, "repeated_count=100", "Should have repeated_count for duplicates")
}

func TestDedupHandler_BatchFlush(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewTextHandler(&buf, nil)

	cfg := DedupHandlerConfig{
		BatchSize:    5,                // Small batch for testing
		FlushTimeout: 10 * time.Second, // Long timeout, won't trigger
	}
	dh := NewDedupHandlerWithConfig(baseHandler, cfg)

	logger := slog.New(dh)

	// Log 5 unique messages (will trigger batch flush)
	for i := 0; i < 5; i++ {
		logger.Info("message", "num", i)
	}

	// Close flushes pending messages and stops background goroutine
	err := dh.Close()
	require.NoError(t, err)

	content := buf.String()

	// All 5 should be flushed due to batch size
	assert.Equal(t, 5, strings.Count(content, "msg=message"), "All messages should be flushed when batch is full")
}

func TestDedupHandler_GracefulClose(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewTextHandler(&buf, nil)

	cfg := DedupHandlerConfig{
		BatchSize:    1000,             // Large batch, won't trigger
		FlushTimeout: 10 * time.Second, // Long timeout, won't trigger
	}
	dh := NewDedupHandlerWithConfig(baseHandler, cfg)

	logger := slog.New(dh)

	// Log some messages
	logger.Info("message 1")
	logger.Info("message 2")

	// Close should flush pending messages
	err := dh.Close()
	require.NoError(t, err)

	content := buf.String()

	// All messages should be flushed on close
	assert.Contains(t, content, "message 1")
	assert.Contains(t, content, "message 2")
}

func TestDedupHandler_DoubleClose(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewTextHandler(&buf, nil)
	dh := NewDedupHandler(baseHandler)

	// First close should succeed
	err := dh.Close()
	require.NoError(t, err)

	// Second close should not panic and return nil
	err = dh.Close()
	require.NoError(t, err)
}

func TestDedupHandler_Enabled(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})

	dh := NewDedupHandler(baseHandler)
	defer dh.Close()

	// Should respect underlying handler's level
	assert.False(t, dh.Enabled(context.Background(), slog.LevelInfo))
	assert.True(t, dh.Enabled(context.Background(), slog.LevelWarn))
	assert.True(t, dh.Enabled(context.Background(), slog.LevelError))
}

func TestDedupHandler_DifferentLevels(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewTextHandler(&buf, nil)

	cfg := DedupHandlerConfig{
		BatchSize:    100,
		FlushTimeout: 50 * time.Millisecond,
	}
	dh := NewDedupHandlerWithConfig(baseHandler, cfg)

	logger := slog.New(dh)

	// Same message, different levels should NOT be deduplicated
	logger.Info("test message")
	logger.Warn("test message")
	logger.Error("test message")

	// Close flushes pending messages and stops background goroutine
	err := dh.Close()
	require.NoError(t, err)

	content := buf.String()

	// Should have 3 messages (different levels)
	assert.Equal(t, 3, strings.Count(content, "test message"),
		"Messages with different levels should not be deduplicated")
}

// syncWriter is a thread-safe writer for testing
type syncWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (sw *syncWriter) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(p)
}
