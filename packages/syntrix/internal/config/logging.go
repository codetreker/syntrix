package config

import (
	"fmt"
	"path/filepath"

	services "github.com/codetreker/syntrix/internal/services/config"
)

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level    string         `yaml:"level"`  // debug, info, warn, error
	Format   string         `yaml:"format"` // text, json
	Dir      string         `yaml:"dir"`    // log directory path
	Rotation RotationConfig `yaml:"rotation"`
	Console  ConsoleConfig  `yaml:"console"`
	File     FileConfig     `yaml:"file"`
	Async    AsyncConfig    `yaml:"async"`
	Dedup    DedupConfig    `yaml:"dedup"`
}

// RotationConfig holds log rotation settings
type RotationConfig struct {
	MaxSize    int  `yaml:"max_size"`    // MB
	MaxBackups int  `yaml:"max_backups"` // number of files
	MaxAge     int  `yaml:"max_age"`     // days
	Compress   bool `yaml:"compress"`    // gzip old files
}

// ConsoleConfig holds console output configuration
type ConsoleConfig struct {
	Enabled bool   `yaml:"enabled"`
	Level   string `yaml:"level"`  // optional override
	Format  string `yaml:"format"` // text or json
}

// FileConfig holds file output configuration
type FileConfig struct {
	Enabled bool   `yaml:"enabled"`
	Level   string `yaml:"level"`  // optional override
	Format  string `yaml:"format"` // text or json
}

// AsyncConfig holds async logging configuration
type AsyncConfig struct {
	Enabled      bool `yaml:"enabled"`       // enable async logging
	BufferSize   int  `yaml:"buffer_size"`   // channel buffer size (default: 10000)
	BatchSize    int  `yaml:"batch_size"`    // batch size before flush (default: 100)
	FlushTimeout int  `yaml:"flush_timeout"` // flush timeout in milliseconds (default: 100)
}

// DedupConfig holds deduplication configuration
type DedupConfig struct {
	Enabled      bool `yaml:"enabled"`       // enable log deduplication
	BatchSize    int  `yaml:"batch_size"`    // batch size before flush (default: 100)
	FlushTimeout int  `yaml:"flush_timeout"` // flush timeout in milliseconds (default: 1000)
}

// DefaultLoggingConfig returns default logging configuration
func DefaultLoggingConfig() LoggingConfig {
	return LoggingConfig{
		Level:  "info",
		Format: "text",
		Dir:    "logs",
		Rotation: RotationConfig{
			MaxSize:    100,
			MaxBackups: 10,
			MaxAge:     30,
			Compress:   true,
		},
		Console: ConsoleConfig{
			Enabled: true,
			Level:   "info",
			Format:  "text",
		},
		File: FileConfig{
			Enabled: true,
			Level:   "info",
			Format:  "text",
		},
		Async: AsyncConfig{
			Enabled:      false, // Disabled by default for compatibility
			BufferSize:   10000,
			BatchSize:    100,
			FlushTimeout: 100,
		},
		Dedup: DedupConfig{
			Enabled:      false, // Disabled by default for simplicity
			BatchSize:    100,
			FlushTimeout: 1000,
		},
	}
}

// ApplyDefaults fills in missing values with defaults
func (c *LoggingConfig) ApplyDefaults() {
	if c.Level == "" {
		c.Level = "info"
	}
	if c.Format == "" {
		c.Format = "text"
	}
	if c.Dir == "" {
		c.Dir = "logs"
	}

	// Rotation defaults
	if c.Rotation.MaxSize == 0 {
		c.Rotation.MaxSize = 100
	}
	if c.Rotation.MaxBackups == 0 {
		c.Rotation.MaxBackups = 10
	}
	if c.Rotation.MaxAge == 0 {
		c.Rotation.MaxAge = 30
	}
	// Note: Compress defaults to false (zero value for bool).
	// DefaultLoggingConfig() returns true as the production default, but
	// ApplyDefaults() cannot distinguish between explicit false and zero value,
	// so users must explicitly set Compress in their config if they want compression.

	// Console defaults - check if console config is completely empty
	if c.Console.Level == "" && c.Console.Format == "" && !c.Console.Enabled {
		c.Console.Enabled = true
	}
	if c.Console.Level == "" {
		c.Console.Level = c.Level
	}
	if c.Console.Format == "" {
		c.Console.Format = c.Format
	}

	// File defaults - check if file config is completely empty
	if c.File.Level == "" && c.File.Format == "" && !c.File.Enabled {
		c.File.Enabled = true
	}
	if c.File.Level == "" {
		c.File.Level = c.Level
	}
	if c.File.Format == "" {
		c.File.Format = c.Format
	}

	// Async defaults
	if c.Async.BufferSize == 0 {
		c.Async.BufferSize = 10000
	}
	if c.Async.BatchSize == 0 {
		c.Async.BatchSize = 100
	}
	if c.Async.FlushTimeout == 0 {
		c.Async.FlushTimeout = 100
	}
}

// ApplyEnvOverrides applies environment variable overrides
func (c *LoggingConfig) ApplyEnvOverrides() {
	// Future: Add environment variable support
	_ = c
}

// ResolvePaths resolves relative paths using the given directories.
// - configDir: not used for logging config (no config-related paths)
// - dataDir: base directory for log files
func (c *LoggingConfig) ResolvePaths(configDir, dataDir string) {
	_ = configDir // logging has no config-related paths
	if c.Dir != "" && !filepath.IsAbs(c.Dir) {
		c.Dir = filepath.Join(dataDir, c.Dir)
	}
}

// Validate validates the configuration
func (c *LoggingConfig) Validate(mode services.DeploymentMode) error {
	validLevels := map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
	}

	if !validLevels[c.Level] {
		return fmt.Errorf("invalid log level: %s (must be debug, info, warn, or error)", c.Level)
	}

	validFormats := map[string]bool{
		"text": true,
		"json": true,
	}

	if !validFormats[c.Format] {
		return fmt.Errorf("invalid log format: %s (must be text or json)", c.Format)
	}

	if c.Dir == "" {
		return fmt.Errorf("log directory cannot be empty")
	}

	// Validate console config if enabled
	if c.Console.Enabled {
		if c.Console.Level != "" && !validLevels[c.Console.Level] {
			return fmt.Errorf("invalid console log level: %s", c.Console.Level)
		}
		if c.Console.Format != "" && !validFormats[c.Console.Format] {
			return fmt.Errorf("invalid console log format: %s", c.Console.Format)
		}
	}

	// Validate file config if enabled
	if c.File.Enabled {
		if c.File.Level != "" && !validLevels[c.File.Level] {
			return fmt.Errorf("invalid file log level: %s", c.File.Level)
		}
		if c.File.Format != "" && !validFormats[c.File.Format] {
			return fmt.Errorf("invalid file log format: %s", c.File.Format)
		}
	}

	// Validate async config if enabled
	if c.Async.Enabled {
		if c.Async.BufferSize <= 0 {
			return fmt.Errorf("async buffer size must be positive: %d", c.Async.BufferSize)
		}
		if c.Async.BatchSize <= 0 {
			return fmt.Errorf("async batch size must be positive: %d", c.Async.BatchSize)
		}
		if c.Async.FlushTimeout <= 0 {
			return fmt.Errorf("async flush timeout must be positive: %d", c.Async.FlushTimeout)
		}
	}

	return nil
}
