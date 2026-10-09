// tests/integration/logging_test.go
//go:build integration
// +build integration

package integration

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogging_FileRotation(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.DefaultLoggingConfig()
	cfg.Dir = tmpDir
	cfg.Rotation.MaxSize = 1 // 1MB for faster testing
	cfg.Console.Enabled = false

	err := logging.Initialize(cfg)
	require.NoError(t, err)
	defer logging.Shutdown()

	// Generate enough logs to trigger rotation
	largeMessage := make([]byte, 10*1024) // 10KB message
	for i := 0; i < len(largeMessage); i++ {
		largeMessage[i] = 'A'
	}

	// Write ~2MB of logs (should trigger rotation)
	for i := 0; i < 200; i++ {
		slog.Info("large message", "data", string(largeMessage), "iteration", i)
	}

	// Give rotation time to happen
	time.Sleep(100 * time.Millisecond)

	// Check that rotated files exist
	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)

	fileCount := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			fileCount++
			t.Logf("Log file: %s", entry.Name())
		}
	}

	// Should have at least main log + rotated file (errors.log may not exist if no errors logged)
	assert.GreaterOrEqual(t, fileCount, 2, "Expected multiple log files due to rotation")
}

func TestLogging_ErrorSeparation(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.DefaultLoggingConfig()
	cfg.Dir = tmpDir
	cfg.Console.Enabled = false

	err := logging.Initialize(cfg)
	require.NoError(t, err)

	// Log various levels
	slog.Debug("debug message")
	slog.Info("info message")
	slog.Warn("warning message")
	slog.Error("error message")

	// Shutdown to flush
	logging.Shutdown()

	// Read main log
	mainLog := filepath.Join(tmpDir, "syntrix.log")
	mainContent, err := os.ReadFile(mainLog)
	require.NoError(t, err)

	// Main log should have info, warn, error (debug filtered by default)
	assert.Contains(t, string(mainContent), "info message")
	assert.Contains(t, string(mainContent), "warning message")
	assert.Contains(t, string(mainContent), "error message")

	// Read error log
	errorLog := filepath.Join(tmpDir, "errors.log")
	errorContent, err := os.ReadFile(errorLog)
	require.NoError(t, err)

	// Error log should only have warn and error
	assert.NotContains(t, string(errorContent), "info message")
	assert.Contains(t, string(errorContent), "warning message")
	assert.Contains(t, string(errorContent), "error message")
}

func TestLogging_JSONFormat(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.DefaultLoggingConfig()
	cfg.Dir = tmpDir
	cfg.Format = "json"
	cfg.File.Format = "json"
	cfg.Console.Enabled = false

	err := logging.Initialize(cfg)
	require.NoError(t, err)

	slog.Info("json test", "key", "value", "number", 42)

	logging.Shutdown()

	// Read main log
	mainLog := filepath.Join(tmpDir, "syntrix.log")
	content, err := os.ReadFile(mainLog)
	require.NoError(t, err)

	// Verify JSON format
	assert.Contains(t, string(content), `"msg":"json test"`)
	assert.Contains(t, string(content), `"key":"value"`)
	assert.Contains(t, string(content), `"number":42`)
}
