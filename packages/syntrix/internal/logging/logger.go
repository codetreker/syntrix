// internal/logging/logger.go
package logging

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	// Global state for cleanup
	logFiles      []*lumberjack.Logger
	asyncWriters  []*AsyncWriter
	dedupHandlers []*DedupHandler
	logFilesMu    sync.Mutex

	// exitFunc allows overriding os.Exit for testing
	exitFunc = os.Exit
)

// Initialize sets up the global logger based on configuration
func Initialize(cfg config.LoggingConfig) error {
	logger, err := NewLogger(cfg)
	if err != nil {
		return fmt.Errorf("failed to create logger: %w", err)
	}

	// Set as global default logger
	slog.SetDefault(logger)

	// Redirect standard log package to use slog
	// This ensures log.Printf, log.Fatalf etc. go through slog
	log.SetOutput(&slogWriter{logger: logger})
	log.SetFlags(0) // slog handles formatting

	// Log startup message with configuration
	slog.Info("Logging initialized",
		"level", cfg.Level,
		"format", cfg.Format,
		"dir", cfg.Dir,
		"console_enabled", cfg.Console.Enabled,
		"file_enabled", cfg.File.Enabled,
		"async_enabled", cfg.Async.Enabled,
	)

	return nil
}

// slogWriter adapts slog.Logger to io.Writer for standard log package
type slogWriter struct {
	logger *slog.Logger
}

func (w *slogWriter) Write(p []byte) (n int, err error) {
	// Remove trailing newline if present (log package adds one)
	msg := string(p)
	if len(msg) > 0 && msg[len(msg)-1] == '\n' {
		msg = msg[:len(msg)-1]
	}
	w.logger.Info(msg)
	return len(p), nil
}

// Fatal logs an error message and exits the program.
// It ensures all log buffers are flushed before exiting.
func Fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	Shutdown() // Flush all buffers
	exitFunc(1)
}

// NewLogger creates a new logger instance with the given configuration
func NewLogger(cfg config.LoggingConfig) (*slog.Logger, error) {
	// Create log directory if it doesn't exist
	if err := os.MkdirAll(cfg.Dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	var handlers []slog.Handler

	// Add console handler if enabled
	if cfg.Console.Enabled {
		level := parseLevel(cfg.Console.Level)
		handler := createHandler(os.Stdout, cfg.Console.Format, level)
		handlers = append(handlers, handler)
	}

	// Add file handlers if enabled
	if cfg.File.Enabled {
		// Main log file (all levels)
		mainLogPath := filepath.Join(cfg.Dir, "syntrix.log")
		mainFile := &lumberjack.Logger{
			Filename:   mainLogPath,
			MaxSize:    cfg.Rotation.MaxSize,
			MaxBackups: cfg.Rotation.MaxBackups,
			MaxAge:     cfg.Rotation.MaxAge,
			Compress:   cfg.Rotation.Compress,
		}
		registerLogFile(mainFile)

		// Wrap with AsyncWriter if enabled
		var mainWriter io.Writer = mainFile
		if cfg.Async.Enabled {
			asyncWriter := NewAsyncWriterWithConfig(mainFile, AsyncWriterConfig{
				BufferSize:   cfg.Async.BufferSize,
				BatchSize:    cfg.Async.BatchSize,
				FlushTimeout: time.Duration(cfg.Async.FlushTimeout) * time.Millisecond,
			})
			registerAsyncWriter(asyncWriter)
			mainWriter = asyncWriter
		}

		level := parseLevel(cfg.File.Level)
		mainHandler := createHandler(mainWriter, cfg.File.Format, level)
		handlers = append(handlers, mainHandler)

		// Error log file (warn and error only)
		errorLogPath := filepath.Join(cfg.Dir, "errors.log")
		errorFile := &lumberjack.Logger{
			Filename:   errorLogPath,
			MaxSize:    cfg.Rotation.MaxSize,
			MaxBackups: cfg.Rotation.MaxBackups,
			MaxAge:     cfg.Rotation.MaxAge,
			Compress:   cfg.Rotation.Compress,
		}
		registerLogFile(errorFile)

		// Wrap with AsyncWriter if enabled
		var errorWriter io.Writer = errorFile
		if cfg.Async.Enabled {
			asyncWriter := NewAsyncWriterWithConfig(errorFile, AsyncWriterConfig{
				BufferSize:   cfg.Async.BufferSize,
				BatchSize:    cfg.Async.BatchSize,
				FlushTimeout: time.Duration(cfg.Async.FlushTimeout) * time.Millisecond,
			})
			registerAsyncWriter(asyncWriter)
			errorWriter = asyncWriter
		}

		errorHandler := createHandler(errorWriter, cfg.File.Format, slog.LevelWarn)
		errorHandler = NewLevelFilter(errorHandler, slog.LevelWarn)
		handlers = append(handlers, errorHandler)
	}

	// Combine all handlers
	var handler slog.Handler
	if len(handlers) == 1 {
		handler = handlers[0]
	} else {
		handler = NewMultiHandler(handlers...)
	}

	// Wrap with DedupHandler if enabled
	if cfg.Dedup.Enabled {
		dedupHandler := NewDedupHandlerWithConfig(handler, DedupHandlerConfig{
			BatchSize:    cfg.Dedup.BatchSize,
			FlushTimeout: time.Duration(cfg.Dedup.FlushTimeout) * time.Millisecond,
		})
		registerDedupHandler(dedupHandler)
		handler = dedupHandler
	}

	return slog.New(handler), nil
}

// Shutdown gracefully closes all log files and flushes buffers
func Shutdown() error {
	logFilesMu.Lock()
	defer logFilesMu.Unlock()

	// Close dedup handlers first to flush any pending logs
	for _, dedupHandler := range dedupHandlers {
		if err := dedupHandler.Close(); err != nil {
			return fmt.Errorf("failed to close dedup handler: %w", err)
		}
	}

	// Close async writers to ensure all logs are flushed
	for _, asyncWriter := range asyncWriters {
		if err := asyncWriter.Close(); err != nil {
			return fmt.Errorf("failed to close async writer: %w", err)
		}
	}

	for _, logFile := range logFiles {
		if err := logFile.Close(); err != nil {
			return fmt.Errorf("failed to close log file: %w", err)
		}
	}

	logFiles = nil
	asyncWriters = nil
	dedupHandlers = nil
	return nil
}

// Helper functions

func registerLogFile(logFile *lumberjack.Logger) {
	logFilesMu.Lock()
	defer logFilesMu.Unlock()
	logFiles = append(logFiles, logFile)
}

func registerAsyncWriter(asyncWriter *AsyncWriter) {
	logFilesMu.Lock()
	defer logFilesMu.Unlock()
	asyncWriters = append(asyncWriters, asyncWriter)
}

func registerDedupHandler(dedupHandler *DedupHandler) {
	logFilesMu.Lock()
	defer logFilesMu.Unlock()
	dedupHandlers = append(dedupHandlers, dedupHandler)
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func createHandler(w io.Writer, format string, level slog.Level) slog.Handler {
	opts := &slog.HandlerOptions{
		Level: level,
	}

	if format == "json" {
		return slog.NewJSONHandler(w, opts)
	}
	// Use custom text handler with format: <TIME>: [<LEVEL>] <MSG> <attributes>
	return NewTextHandler(w, opts)
}
