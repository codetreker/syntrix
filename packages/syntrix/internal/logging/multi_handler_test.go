// internal/logging/multi_handler_test.go
package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// mockHandler is a test handler that can be configured to fail
type mockHandler struct {
	enabled    bool
	handleErr  error
	handleFunc func(context.Context, slog.Record) error
}

func (h *mockHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return h.enabled
}

func (h *mockHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.handleFunc != nil {
		return h.handleFunc(ctx, r)
	}
	return h.handleErr
}

func (h *mockHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *mockHandler) WithGroup(name string) slog.Handler {
	return h
}

func TestMultiHandler_Handle(t *testing.T) {
	// Create two buffers to capture output
	buf1 := &bytes.Buffer{}
	buf2 := &bytes.Buffer{}

	// Create two handlers
	handler1 := slog.NewTextHandler(buf1, &slog.HandlerOptions{Level: slog.LevelInfo})
	handler2 := slog.NewTextHandler(buf2, &slog.HandlerOptions{Level: slog.LevelInfo})

	// Create multi-handler
	multi := NewMultiHandler(handler1, handler2)

	// Create logger with multi-handler
	logger := slog.New(multi)

	// Log a message
	logger.Info("test message", "key", "value")

	// Both buffers should contain the message
	assert.Contains(t, buf1.String(), "test message")
	assert.Contains(t, buf1.String(), "key=value")
	assert.Contains(t, buf2.String(), "test message")
	assert.Contains(t, buf2.String(), "key=value")
}

func TestMultiHandler_Enabled(t *testing.T) {
	handler1 := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelInfo})
	handler2 := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})

	multi := NewMultiHandler(handler1, handler2)

	// Info level should be enabled (handler1 accepts it)
	assert.True(t, multi.Enabled(context.Background(), slog.LevelInfo))

	// Debug level should be disabled (no handler accepts it)
	assert.False(t, multi.Enabled(context.Background(), slog.LevelDebug))
}

func TestMultiHandler_WithAttrs(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})

	multi := NewMultiHandler(handler)
	multiWithAttrs := multi.WithAttrs([]slog.Attr{slog.String("component", "test")})

	logger := slog.New(multiWithAttrs)
	logger.Info("test message")

	assert.Contains(t, buf.String(), "component=test")
}

func TestMultiHandler_WithGroup(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})

	multi := NewMultiHandler(handler)
	multiWithGroup := multi.WithGroup("request")

	logger := slog.New(multiWithGroup)
	logger.Info("test message", "id", "123")

	assert.Contains(t, buf.String(), "request.id=123")
}

func TestMultiHandler_ErrorHandling(t *testing.T) {
	t.Run("fail-fast on first error", func(t *testing.T) {
		// Track which handlers were called
		var called []int

		handler1 := &mockHandler{
			enabled: true,
			handleFunc: func(_ context.Context, _ slog.Record) error {
				called = append(called, 1)
				return errors.New("handler1 failed")
			},
		}

		handler2 := &mockHandler{
			enabled: true,
			handleFunc: func(_ context.Context, _ slog.Record) error {
				called = append(called, 2)
				return nil
			},
		}

		multi := NewMultiHandler(handler1, handler2)

		// Create a test record
		record := slog.NewRecord(time.Now(), slog.LevelInfo, "test", 0)

		// Handle should fail on first error
		err := multi.Handle(context.Background(), record)

		// Verify error is returned
		assert.Error(t, err)
		assert.EqualError(t, err, "handler1 failed")

		// Verify fail-fast behavior: only handler1 was called
		assert.Equal(t, []int{1}, called, "should fail fast and not call handler2")
	})

	t.Run("disabled handler not called", func(t *testing.T) {
		var called []int

		handler1 := &mockHandler{
			enabled: false, // Disabled
			handleFunc: func(_ context.Context, _ slog.Record) error {
				called = append(called, 1)
				return nil
			},
		}

		handler2 := &mockHandler{
			enabled: true, // Enabled
			handleFunc: func(_ context.Context, _ slog.Record) error {
				called = append(called, 2)
				return nil
			},
		}

		multi := NewMultiHandler(handler1, handler2)

		record := slog.NewRecord(time.Now(), slog.LevelInfo, "test", 0)
		err := multi.Handle(context.Background(), record)

		assert.NoError(t, err)
		assert.Equal(t, []int{2}, called, "only enabled handler should be called")
	})
}

func TestMultiHandler_EmptyHandlers(t *testing.T) {
	t.Run("empty handlers list", func(t *testing.T) {
		// Creating with zero handlers should not panic
		multi := NewMultiHandler()
		assert.NotNil(t, multi)
		assert.Equal(t, 0, len(multi.handlers))

		// Enabled should return false for empty handlers
		enabled := multi.Enabled(context.Background(), slog.LevelInfo)
		assert.False(t, enabled, "empty handler should not be enabled")

		// Handle should succeed with no-op
		record := slog.NewRecord(time.Now(), slog.LevelInfo, "test", 0)
		err := multi.Handle(context.Background(), record)
		assert.NoError(t, err, "empty handler should handle without error")
	})

	t.Run("can use with logger", func(t *testing.T) {
		multi := NewMultiHandler()
		logger := slog.New(multi)

		// Should not panic when logging
		assert.NotPanics(t, func() {
			logger.Info("test message")
		})
	})
}

func TestMultiHandler_WithAttrsChaining(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})

	// Chain multiple WithAttrs calls
	multi := NewMultiHandler(handler)
	multi1 := multi.WithAttrs([]slog.Attr{slog.String("component", "test")})
	multi2 := multi1.WithAttrs([]slog.Attr{slog.String("version", "1.0")})

	logger := slog.New(multi2)
	logger.Info("test message")

	output := buf.String()
	assert.Contains(t, output, "component=test")
	assert.Contains(t, output, "version=1.0")
}

func TestMultiHandler_WithGroupChaining(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})

	// Chain multiple WithGroup calls
	multi := NewMultiHandler(handler)
	multi1 := multi.WithGroup("request")
	multi2 := multi1.WithGroup("user")

	logger := slog.New(multi2)
	logger.Info("test message", "id", "123")

	assert.Contains(t, buf.String(), "request.user.id=123")
}

func TestMultiHandler_WithGroupEmptyString(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})

	multi := NewMultiHandler(handler)
	// slog's TextHandler behavior with empty group name adds a "." prefix
	multiWithGroup := multi.WithGroup("")

	logger := slog.New(multiWithGroup)
	logger.Info("test message", "id", "123")

	// Empty group in TextHandler adds a "." prefix
	output := buf.String()
	assert.Contains(t, output, ".id=123")
}
