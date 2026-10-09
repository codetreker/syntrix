// internal/logging/level_filter_test.go
package logging

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLevelFilter_OnlyErrorsAndWarnings(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	// Create filter that only allows Warn and Error
	errorFilter := NewLevelFilter(handler, slog.LevelWarn)

	logger := slog.New(errorFilter)

	// Log messages at different levels
	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")

	output := buf.String()

	// Only warn and error should appear
	assert.NotContains(t, output, "debug message")
	assert.NotContains(t, output, "info message")
	assert.Contains(t, output, "warn message")
	assert.Contains(t, output, "error message")
}

func TestLevelFilter_Enabled(t *testing.T) {
	handler := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})

	// Create filter that only allows Warn and Error
	errorFilter := NewLevelFilter(handler, slog.LevelWarn)

	ctx := context.Background()

	// Debug and Info should be disabled
	assert.False(t, errorFilter.Enabled(ctx, slog.LevelDebug))
	assert.False(t, errorFilter.Enabled(ctx, slog.LevelInfo))

	// Warn and Error should be enabled
	assert.True(t, errorFilter.Enabled(ctx, slog.LevelWarn))
	assert.True(t, errorFilter.Enabled(ctx, slog.LevelError))
}

func TestLevelFilter_WithAttrs(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	// Create filter for errors only
	errorFilter := NewLevelFilter(handler, slog.LevelWarn)

	// Add attributes
	filterWithAttrs := errorFilter.WithAttrs([]slog.Attr{slog.String("component", "test")})

	logger := slog.New(filterWithAttrs)
	logger.Warn("warning message")

	output := buf.String()
	assert.Contains(t, output, "component=test")
	assert.Contains(t, output, "warning message")
}

func TestLevelFilter_WithGroup(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	// Create filter for errors only
	errorFilter := NewLevelFilter(handler, slog.LevelWarn)

	// Add group
	filterWithGroup := errorFilter.WithGroup("request")

	logger := slog.New(filterWithGroup)
	logger.Error("error message", "id", "123")

	output := buf.String()
	assert.Contains(t, output, "request.id=123")
	assert.Contains(t, output, "error message")
}

func TestLevelFilter_HandleRespectsMinLevel(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	// Create filter that only allows Error
	errorFilter := NewLevelFilter(handler, slog.LevelError)

	logger := slog.New(errorFilter)

	// Log at different levels
	logger.Info("info message")
	logger.Warn("warn message")
	logger.Error("error message")

	output := buf.String()

	// Only error should appear
	assert.NotContains(t, output, "info message")
	assert.NotContains(t, output, "warn message")
	assert.Contains(t, output, "error message")
}

func TestLevelFilter_WithChaining(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	// Create filter and chain multiple operations
	errorFilter := NewLevelFilter(handler, slog.LevelWarn)
	filter1 := errorFilter.WithAttrs([]slog.Attr{slog.String("component", "test")})
	filter2 := filter1.WithGroup("request")

	logger := slog.New(filter2)
	logger.Error("error message", "id", "123")

	output := buf.String()
	assert.Contains(t, output, "component=test")
	assert.Contains(t, output, "request.id=123")
	assert.Contains(t, output, "error message")
}

func TestLevelFilter_Handle_BelowThreshold(t *testing.T) {
	buf := &bytes.Buffer{}
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	// Create filter that only allows Error level
	errorFilter := NewLevelFilter(handler, slog.LevelError)

	ctx := context.Background()

	// Create a record with level below threshold (Info < Error)
	var pc uintptr
	record := slog.NewRecord(
		time.Now(),
		slog.LevelInfo,
		"info message below threshold",
		pc,
	)

	// Directly call Handle with below-threshold level
	err := errorFilter.Handle(ctx, record)

	// Should return nil without error
	assert.NoError(t, err)

	// Buffer should be empty (message not written)
	assert.Empty(t, buf.String())
}
