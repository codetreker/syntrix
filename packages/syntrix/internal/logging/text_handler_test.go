// internal/logging/text_handler_test.go
package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTextHandler_BasicFormat(t *testing.T) {
	var buf bytes.Buffer
	handler := NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	record := slog.NewRecord(time.Date(2024, 1, 19, 10, 30, 0, 0, time.UTC), slog.LevelInfo, "test message", 0)
	record.AddAttrs(slog.String("key", "value"))

	err := handler.Handle(context.Background(), record)
	require.NoError(t, err)

	output := buf.String()

	// Verify format: <TIME>: [<LEVEL>] <MSG> <attributes>
	assert.Contains(t, output, "2024-01-19T10:30:00Z: [INFO] test message key=value")
}

func TestTextHandler_DifferentLevels(t *testing.T) {
	tests := []struct {
		name     string
		level    slog.Level
		expected string
	}{
		{
			name:     "debug level",
			level:    slog.LevelDebug,
			expected: ": [DEBUG] ",
		},
		{
			name:     "info level",
			level:    slog.LevelInfo,
			expected: ": [INFO] ",
		},
		{
			name:     "warn level",
			level:    slog.LevelWarn,
			expected: ": [WARN] ",
		},
		{
			name:     "error level",
			level:    slog.LevelError,
			expected: ": [ERROR] ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			handler := NewTextHandler(&buf, &slog.HandlerOptions{
				Level: slog.LevelDebug, // Allow all levels
			})

			record := slog.NewRecord(time.Now(), tt.level, "test", 0)
			err := handler.Handle(context.Background(), record)
			require.NoError(t, err)

			assert.Contains(t, buf.String(), tt.expected)
		})
	}
}

func TestTextHandler_MultipleAttributes(t *testing.T) {
	var buf bytes.Buffer
	handler := NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "message", 0)
	record.AddAttrs(
		slog.String("string", "value"),
		slog.Int("int", 42),
		slog.Bool("bool", true),
		slog.Float64("float", 3.14),
	)

	err := handler.Handle(context.Background(), record)
	require.NoError(t, err)

	output := buf.String()

	assert.Contains(t, output, "string=value")
	assert.Contains(t, output, "int=42")
	assert.Contains(t, output, "bool=true")
	assert.Contains(t, output, "float=3.14")
}

func TestTextHandler_StringQuoting(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected string
	}{
		{
			name:     "simple string no quotes",
			value:    "simple",
			expected: "key=simple",
		},
		{
			name:     "string with spaces",
			value:    "hello world",
			expected: `key="hello world"`,
		},
		{
			name:     "string with quotes",
			value:    `hello "world"`,
			expected: `key="hello \"world\""`,
		},
		{
			name:     "string with newline",
			value:    "hello\nworld",
			expected: `key="hello\nworld"`,
		},
		{
			name:     "string with tab",
			value:    "hello\tworld",
			expected: `key="hello\tworld"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			handler := NewTextHandler(&buf, &slog.HandlerOptions{
				Level: slog.LevelInfo,
			})

			record := slog.NewRecord(time.Now(), slog.LevelInfo, "test", 0)
			record.AddAttrs(slog.String("key", tt.value))

			err := handler.Handle(context.Background(), record)
			require.NoError(t, err)

			assert.Contains(t, buf.String(), tt.expected)
		})
	}
}

func TestTextHandler_WithAttrs(t *testing.T) {
	var buf bytes.Buffer
	handler := NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	// Add persistent attributes
	handlerWithAttrs := handler.WithAttrs([]slog.Attr{
		slog.String("service", "api"),
		slog.String("version", "1.0"),
	})

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "message", 0)
	record.AddAttrs(slog.String("request", "GET"))

	err := handlerWithAttrs.Handle(context.Background(), record)
	require.NoError(t, err)

	output := buf.String()

	// Persistent attributes should appear first
	assert.Contains(t, output, "service=api")
	assert.Contains(t, output, "version=1.0")
	assert.Contains(t, output, "request=GET")
}

func TestTextHandler_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	handler := NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	// Add group
	handlerWithGroup := handler.WithGroup("server")

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "started", 0)
	record.AddAttrs(
		slog.Int("port", 8080),
		slog.String("host", "localhost"),
	)

	err := handlerWithGroup.Handle(context.Background(), record)
	require.NoError(t, err)

	output := buf.String()

	// Attributes should have group prefix
	assert.Contains(t, output, "server.port=8080")
	assert.Contains(t, output, "server.host=localhost")
}

func TestTextHandler_NestedGroups(t *testing.T) {
	var buf bytes.Buffer
	handler := NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	// Add nested groups
	handlerWithGroup := handler.WithGroup("app").WithGroup("server")

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "event", 0)
	record.AddAttrs(slog.String("status", "ok"))

	err := handlerWithGroup.Handle(context.Background(), record)
	require.NoError(t, err)

	output := buf.String()

	// Should have nested group prefix
	assert.Contains(t, output, "app.server.status=ok")
}

func TestTextHandler_EmptyGroupName(t *testing.T) {
	var buf bytes.Buffer
	handler := NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	// Empty group name should be ignored
	handlerWithGroup := handler.WithGroup("")

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "message", 0)
	record.AddAttrs(slog.String("key", "value"))

	err := handlerWithGroup.Handle(context.Background(), record)
	require.NoError(t, err)

	output := buf.String()

	// Should not have any group prefix
	assert.Contains(t, output, "key=value")
	assert.NotContains(t, output, ".key=value")
}

func TestTextHandler_GroupValue(t *testing.T) {
	var buf bytes.Buffer
	handler := NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "message", 0)
	record.AddAttrs(slog.Group("details",
		slog.String("name", "test"),
		slog.Int("count", 10),
	))

	err := handler.Handle(context.Background(), record)
	require.NoError(t, err)

	output := buf.String()

	// Group value should be formatted as {key=value ...}
	assert.Contains(t, output, "details={name=test count=10}")
}

func TestTextHandler_Enabled(t *testing.T) {
	handler := NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})

	tests := []struct {
		name     string
		level    slog.Level
		expected bool
	}{
		{
			name:     "debug below threshold",
			level:    slog.LevelDebug,
			expected: false,
		},
		{
			name:     "info below threshold",
			level:    slog.LevelInfo,
			expected: false,
		},
		{
			name:     "warn at threshold",
			level:    slog.LevelWarn,
			expected: true,
		},
		{
			name:     "error above threshold",
			level:    slog.LevelError,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enabled := handler.Enabled(context.Background(), tt.level)
			assert.Equal(t, tt.expected, enabled)
		})
	}
}

func TestTextHandler_DifferentValueTypes(t *testing.T) {
	var buf bytes.Buffer
	handler := NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	now := time.Date(2024, 1, 19, 10, 30, 0, 0, time.UTC)
	duration := 5 * time.Second

	record := slog.NewRecord(now, slog.LevelInfo, "types", 0)
	record.AddAttrs(
		slog.Int64("int64", 123456789),
		slog.Uint64("uint64", 987654321),
		slog.Float64("float", 2.718),
		slog.Bool("bool", false),
		slog.Time("time", now),
		slog.Duration("duration", duration),
		slog.Any("any", struct{ Name string }{Name: "test"}),
	)

	err := handler.Handle(context.Background(), record)
	require.NoError(t, err)

	output := buf.String()

	assert.Contains(t, output, "int64=123456789")
	assert.Contains(t, output, "uint64=987654321")
	assert.Contains(t, output, "float=2.718")
	assert.Contains(t, output, "bool=false")
	assert.Contains(t, output, "time=2024-01-19T10:30:00Z")
	assert.Contains(t, output, "duration=5s")
	assert.Contains(t, output, "any={Name:test}")
}

func TestTextHandler_NoAttributes(t *testing.T) {
	var buf bytes.Buffer
	handler := NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "simple message", 0)

	err := handler.Handle(context.Background(), record)
	require.NoError(t, err)

	output := buf.String()

	// Should contain message without attributes
	assert.Contains(t, output, ": [INFO] simple message")
	assert.True(t, strings.HasSuffix(output, "simple message\n"))
}

func TestTextHandler_ConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	handler := NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	// Test concurrent writes
	const numGoroutines = 10
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			record := slog.NewRecord(time.Now(), slog.LevelInfo, "concurrent", 0)
			record.AddAttrs(slog.Int("id", id))
			handler.Handle(context.Background(), record)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	output := buf.String()

	// Should have written all messages
	lines := strings.Split(strings.TrimSpace(output), "\n")
	assert.Len(t, lines, numGoroutines)
}
