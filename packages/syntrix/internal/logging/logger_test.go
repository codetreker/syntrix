// internal/logging/logger_test.go
package logging

import (
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/codetreker/syntrix/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLogger_DefaultConfig(t *testing.T) {
	cfg := config.DefaultLoggingConfig()

	// Use temp directory for testing
	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)
	assert.NotNil(t, logger)

	// Test that logging works
	logger.Info("test message", "key", "value")

	// Verify main log file was created
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	assert.FileExists(t, mainLogPath)

	// Note: error log file won't exist yet since we only logged Info
	// and lumberjack doesn't create empty files

	// Cleanup
	Shutdown()
}

func TestNewLogger_JSONFormat(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Format = "json"
	cfg.File.Format = "json"

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)

	logger.Info("test json", "key", "value")

	// Read log file and verify JSON format
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	content, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	assert.Contains(t, string(content), `"msg":"test json"`)
	assert.Contains(t, string(content), `"key":"value"`)

	Shutdown()
}

func TestNewLogger_ErrorLogSeparation(t *testing.T) {
	cfg := config.DefaultLoggingConfig()

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)

	// Log different levels
	logger.Info("info message")
	logger.Warn("warning message")
	logger.Error("error message")

	// Shutdown to flush buffers
	Shutdown()

	// Check main log (should have all messages)
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	mainContent, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	assert.Contains(t, string(mainContent), "info message")
	assert.Contains(t, string(mainContent), "warning message")
	assert.Contains(t, string(mainContent), "error message")

	// Check error log (should only have warn and error)
	errorLogPath := filepath.Join(tmpDir, "errors.log")
	errorContent, err := os.ReadFile(errorLogPath)
	require.NoError(t, err)

	assert.NotContains(t, string(errorContent), "info message")
	assert.Contains(t, string(errorContent), "warning message")
	assert.Contains(t, string(errorContent), "error message")
}

func TestNewLogger_ConsoleDisabled(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Console.Enabled = false

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)
	assert.NotNil(t, logger)

	// Logging should still work (to files only)
	logger.Info("test message")

	Shutdown()

	// Verify file was created
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	assert.FileExists(t, mainLogPath)
}

func TestInitialize_SetsGlobalLogger(t *testing.T) {
	cfg := config.DefaultLoggingConfig()

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	err := Initialize(cfg)
	require.NoError(t, err)

	// Test that global default logger works
	slog.Info("global test message")

	Shutdown()

	// Verify log was written
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	content, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	assert.Contains(t, string(content), "global test message")
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name     string
		level    string
		expected slog.Level
	}{
		{
			name:     "debug level",
			level:    "debug",
			expected: slog.LevelDebug,
		},
		{
			name:     "info level",
			level:    "info",
			expected: slog.LevelInfo,
		},
		{
			name:     "warn level",
			level:    "warn",
			expected: slog.LevelWarn,
		},
		{
			name:     "error level",
			level:    "error",
			expected: slog.LevelError,
		},
		{
			name:     "unknown level defaults to info",
			level:    "invalid",
			expected: slog.LevelInfo,
		},
		{
			name:     "empty level defaults to info",
			level:    "",
			expected: slog.LevelInfo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseLevel(tt.level)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNewLogger_CustomTextFormat(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Format = "text"
	cfg.File.Format = "text"

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)

	// Log with different levels and attributes
	logger.Info("test message", "key", "value")
	logger.Warn("warning message", "code", 42)
	logger.Error("error message", "error", "something failed", "retry", true)

	// Shutdown to flush buffers
	Shutdown()

	// Read log file and verify custom format
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	content, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	lines := string(content)

	// Verify format: <TIME>: [<LEVEL>] <MSG> <attributes>
	// Example: 2024-01-19T10:30:00Z: [INFO] test message key=value

	// Check INFO line
	assert.Contains(t, lines, ": [INFO] test message key=value")

	// Check WARN line
	assert.Contains(t, lines, ": [WARN] warning message code=42")

	// Check ERROR line
	assert.Contains(t, lines, `: [ERROR] error message error="something failed" retry=true`)
}

func TestNewLogger_TextFormatWithGroups(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Format = "text"
	cfg.File.Format = "text"

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)

	// Log with groups
	groupLogger := logger.WithGroup("server")
	groupLogger.Info("started", "port", 8080, "host", "localhost")

	// Shutdown to flush buffers
	Shutdown()

	// Read log file and verify format with groups
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	content, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	lines := string(content)

	// Verify group prefix
	assert.Contains(t, lines, ": [INFO] started server.port=8080 server.host=localhost")
}

func TestNewLogger_TextFormatWithAttrs(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Format = "text"
	cfg.File.Format = "text"

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)

	// Log with persistent attributes
	attrLogger := logger.With("service", "api", "version", "1.0")
	attrLogger.Info("request processed", "method", "GET", "path", "/users")

	// Shutdown to flush buffers
	Shutdown()

	// Read log file and verify format with attributes
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	content, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	lines := string(content)

	// Verify persistent attributes are included
	assert.Contains(t, lines, ": [INFO] request processed service=api version=1.0 method=GET path=/users")
}

func TestNewLogger_AsyncEnabled(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Async.Enabled = true
	cfg.Async.BufferSize = 1000
	cfg.Async.BatchSize = 10
	cfg.Async.FlushTimeout = 50

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)
	assert.NotNil(t, logger)

	// Write multiple messages quickly
	for i := 0; i < 100; i++ {
		logger.Info("async test message", "index", i)
	}

	// Shutdown to ensure all messages are flushed
	err = Shutdown()
	assert.NoError(t, err)

	// Verify log file was created and has content
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	assert.FileExists(t, mainLogPath)

	content, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	// Verify some messages were written
	lines := string(content)
	assert.Contains(t, lines, "async test message")
	assert.Contains(t, lines, "index=0")
	assert.Contains(t, lines, "index=99")
}

func TestNewLogger_AsyncDisabled(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Async.Enabled = false

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)
	assert.NotNil(t, logger)

	// Write messages
	for i := 0; i < 10; i++ {
		logger.Info("sync test message", "index", i)
	}

	// Shutdown
	err = Shutdown()
	assert.NoError(t, err)

	// Verify log file was created
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	assert.FileExists(t, mainLogPath)

	content, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	lines := string(content)
	assert.Contains(t, lines, "sync test message")
}

func TestNewLogger_DedupEnabled(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Dedup.Enabled = true
	cfg.Dedup.BatchSize = 5
	cfg.Dedup.FlushTimeout = 100

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)
	assert.NotNil(t, logger)

	// Write duplicate messages
	for i := 0; i < 10; i++ {
		logger.Info("duplicate message", "key", "value")
	}

	// Shutdown to flush
	err = Shutdown()
	assert.NoError(t, err)

	// Verify log file was created
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	assert.FileExists(t, mainLogPath)
}

func TestSlogWriter_Write(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Console.Enabled = false // Disable console to avoid noise

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)

	writer := &slogWriter{logger: logger}

	// Test writing a message with newline (like standard log package)
	msg := "test message from log package\n"
	n, err := writer.Write([]byte(msg))
	assert.NoError(t, err)
	assert.Equal(t, len(msg), n) // Should return length of input

	// Test writing a message without newline
	msg2 := "message without newline"
	n, err = writer.Write([]byte(msg2))
	assert.NoError(t, err)
	assert.Equal(t, len(msg2), n)

	// Shutdown and verify logs
	Shutdown()

	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	content, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	lines := string(content)
	// The message should be logged without the trailing newline
	assert.Contains(t, lines, "test message from log package")
	assert.Contains(t, lines, "message without newline")
}

func TestSlogWriter_Write_EmptyMessage(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Console.Enabled = false

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	logger, err := NewLogger(cfg)
	require.NoError(t, err)

	writer := &slogWriter{logger: logger}

	// Test writing empty message
	n, err := writer.Write([]byte(""))
	assert.NoError(t, err)
	assert.Equal(t, 0, n)

	// Test writing just a newline
	n, err = writer.Write([]byte("\n"))
	assert.NoError(t, err)
	assert.Equal(t, 1, n)

	Shutdown()
}

func TestInitialize_RedirectsStandardLog(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Console.Enabled = false // Disable console to avoid noise

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	err := Initialize(cfg)
	require.NoError(t, err)

	// Use standard log package - this should be redirected to slog
	log.Print("message from standard log")
	log.Printf("formatted message: %d", 42)

	// Shutdown to flush
	Shutdown()

	// Verify logs were captured
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	content, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	lines := string(content)
	assert.Contains(t, lines, "message from standard log")
	assert.Contains(t, lines, "formatted message: 42")
}

func TestFatal(t *testing.T) {
	cfg := config.DefaultLoggingConfig()
	cfg.Console.Enabled = false

	tmpDir := t.TempDir()
	cfg.Dir = tmpDir

	err := Initialize(cfg)
	require.NoError(t, err)

	// Override exitFunc to capture the exit code instead of actually exiting
	var exitCode int
	originalExitFunc := exitFunc
	exitFunc = func(code int) {
		exitCode = code
	}
	defer func() {
		exitFunc = originalExitFunc
	}()

	// Call Fatal
	Fatal("fatal error occurred", "key", "value")

	// Verify exit code
	assert.Equal(t, 1, exitCode)

	// Shutdown to flush (Fatal already called Shutdown, but be safe)
	Shutdown()

	// Verify the error was logged
	mainLogPath := filepath.Join(tmpDir, "syntrix.log")
	content, err := os.ReadFile(mainLogPath)
	require.NoError(t, err)

	lines := string(content)
	assert.Contains(t, lines, "fatal error occurred")
	assert.Contains(t, lines, "key=value")
}
