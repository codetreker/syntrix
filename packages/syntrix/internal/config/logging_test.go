package config

import (
	"testing"

	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func TestDefaultLoggingConfig(t *testing.T) {
	cfg := DefaultLoggingConfig()

	assert.Equal(t, "info", cfg.Level)
	assert.Equal(t, "text", cfg.Format)
	assert.Equal(t, "logs", cfg.Dir)
	assert.Equal(t, 100, cfg.Rotation.MaxSize)
	assert.Equal(t, 10, cfg.Rotation.MaxBackups)
	assert.Equal(t, 30, cfg.Rotation.MaxAge)
	assert.True(t, cfg.Rotation.Compress)
	assert.True(t, cfg.Console.Enabled)
	assert.True(t, cfg.File.Enabled)
}

func TestLoggingConfigYAMLParsing(t *testing.T) {
	yamlData := `
level: "debug"
format: "json"
dir: "/var/log/syntrix"
rotation:
  max_size: 50
  max_backups: 5
  max_age: 14
  compress: false
console:
  enabled: false
  level: "warn"
  format: "text"
file:
  enabled: true
  level: "info"
  format: "json"
`

	var cfg LoggingConfig
	err := yaml.Unmarshal([]byte(yamlData), &cfg)

	assert.NoError(t, err)
	assert.Equal(t, "debug", cfg.Level)
	assert.Equal(t, "json", cfg.Format)
	assert.Equal(t, "/var/log/syntrix", cfg.Dir)
	assert.Equal(t, 50, cfg.Rotation.MaxSize)
	assert.False(t, cfg.Console.Enabled)
}

func TestLoggingConfigApplyDefaults(t *testing.T) {
	cfg := &LoggingConfig{}
	cfg.ApplyDefaults()

	assert.Equal(t, "info", cfg.Level)
	assert.Equal(t, "text", cfg.Format)
	assert.Equal(t, "logs", cfg.Dir)
	assert.Equal(t, 100, cfg.Rotation.MaxSize)
	assert.Equal(t, 10, cfg.Rotation.MaxBackups)
	assert.Equal(t, 30, cfg.Rotation.MaxAge)
	// Note: Compress defaults to false (zero value), users must explicitly set to true
	assert.False(t, cfg.Rotation.Compress)
	assert.True(t, cfg.Console.Enabled)
	assert.True(t, cfg.File.Enabled)
}

func TestLoggingConfigApplyDefaultsWithPartialConfig(t *testing.T) {
	cfg := &LoggingConfig{
		Level:  "debug",
		Format: "json",
		Console: ConsoleConfig{
			Enabled: true,
			Level:   "warn",
		},
	}
	cfg.ApplyDefaults()

	// Explicitly set values should be preserved
	assert.Equal(t, "debug", cfg.Level)
	assert.Equal(t, "json", cfg.Format)
	assert.Equal(t, "warn", cfg.Console.Level)

	// Missing values should be filled with defaults
	assert.Equal(t, "logs", cfg.Dir)
	assert.Equal(t, 100, cfg.Rotation.MaxSize)
	assert.Equal(t, "json", cfg.Console.Format) // inherits from cfg.Format
	assert.True(t, cfg.File.Enabled)
	assert.Equal(t, "debug", cfg.File.Level) // inherits from cfg.Level
	assert.Equal(t, "json", cfg.File.Format) // inherits from cfg.Format
}

func TestLoggingConfigResolvePaths(t *testing.T) {
	tests := []struct {
		name      string
		configDir string
		dataDir   string
		dir       string
		expected  string
	}{
		{
			name:      "relative path resolved from data_dir",
			configDir: "/app/configs",
			dataDir:   "/app/data",
			dir:       "logs",
			expected:  "/app/data/logs",
		},
		{
			name:      "absolute path unchanged",
			configDir: "/app/configs",
			dataDir:   "/app/data",
			dir:       "/var/log/syntrix",
			expected:  "/var/log/syntrix",
		},
		{
			name:      "relative with subdirs",
			configDir: "/app/configs",
			dataDir:   "/app/data",
			dir:       "logs/app",
			expected:  "/app/data/logs/app",
		},
		{
			name:      "empty dir unchanged",
			configDir: "/app/configs",
			dataDir:   "/app/data",
			dir:       "",
			expected:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &LoggingConfig{Dir: tt.dir}
			cfg.ResolvePaths(tt.configDir, tt.dataDir)
			assert.Equal(t, tt.expected, cfg.Dir)
		})
	}
}

func TestLoggingConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		cfg         LoggingConfig
		expectError bool
	}{
		{
			name: "valid config",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "logs",
			},
			expectError: false,
		},
		{
			name: "invalid level",
			cfg: LoggingConfig{
				Level:  "invalid",
				Format: "text",
				Dir:    "logs",
			},
			expectError: true,
		},
		{
			name: "invalid format",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "xml",
				Dir:    "logs",
			},
			expectError: true,
		},
		{
			name: "empty dir",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "",
			},
			expectError: true,
		},
		{
			name: "invalid console level when enabled",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "logs",
				Console: ConsoleConfig{
					Enabled: true,
					Level:   "invalid",
					Format:  "text",
				},
			},
			expectError: true,
		},
		{
			name: "invalid console format when enabled",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "logs",
				Console: ConsoleConfig{
					Enabled: true,
					Level:   "info",
					Format:  "xml",
				},
			},
			expectError: true,
		},
		{
			name: "invalid file level when enabled",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "logs",
				File: FileConfig{
					Enabled: true,
					Level:   "invalid",
					Format:  "text",
				},
			},
			expectError: true,
		},
		{
			name: "invalid file format when enabled",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "logs",
				File: FileConfig{
					Enabled: true,
					Level:   "info",
					Format:  "xml",
				},
			},
			expectError: true,
		},
		{
			name: "valid config with console and file overrides",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "logs",
				Console: ConsoleConfig{
					Enabled: true,
					Level:   "debug",
					Format:  "text",
				},
				File: FileConfig{
					Enabled: true,
					Level:   "warn",
					Format:  "json",
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate(services.ModeDistributed)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLoggingConfigApplyEnvOverrides(t *testing.T) {
	// ApplyEnvOverrides is currently a placeholder for future environment variable support.
	// This test verifies it can be called without error and doesn't modify the config.
	cfg := LoggingConfig{
		Level:  "info",
		Format: "text",
		Dir:    "logs",
	}

	// Call should not panic or error
	cfg.ApplyEnvOverrides()

	// Config should remain unchanged (placeholder does nothing)
	assert.Equal(t, "info", cfg.Level)
	assert.Equal(t, "text", cfg.Format)
	assert.Equal(t, "logs", cfg.Dir)
}

func TestAsyncConfig_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     LoggingConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid async config",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "/tmp/logs",
				Async: AsyncConfig{
					Enabled:      true,
					BufferSize:   1000,
					BatchSize:    10,
					FlushTimeout: 100,
				},
			},
			wantErr: false,
		},
		{
			name: "async disabled - no validation",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "/tmp/logs",
				Async: AsyncConfig{
					Enabled:      false,
					BufferSize:   0,
					BatchSize:    0,
					FlushTimeout: 0,
				},
			},
			wantErr: false,
		},
		{
			name: "invalid buffer size",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "/tmp/logs",
				Async: AsyncConfig{
					Enabled:      true,
					BufferSize:   0,
					BatchSize:    10,
					FlushTimeout: 100,
				},
			},
			wantErr: true,
			errMsg:  "async buffer size must be positive",
		},
		{
			name: "invalid batch size",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "/tmp/logs",
				Async: AsyncConfig{
					Enabled:      true,
					BufferSize:   1000,
					BatchSize:    0,
					FlushTimeout: 100,
				},
			},
			wantErr: true,
			errMsg:  "async batch size must be positive",
		},
		{
			name: "invalid flush timeout",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "/tmp/logs",
				Async: AsyncConfig{
					Enabled:      true,
					BufferSize:   1000,
					BatchSize:    10,
					FlushTimeout: 0,
				},
			},
			wantErr: true,
			errMsg:  "async flush timeout must be positive",
		},
		{
			name: "negative buffer size",
			cfg: LoggingConfig{
				Level:  "info",
				Format: "text",
				Dir:    "/tmp/logs",
				Async: AsyncConfig{
					Enabled:      true,
					BufferSize:   -100,
					BatchSize:    10,
					FlushTimeout: 100,
				},
			},
			wantErr: true,
			errMsg:  "async buffer size must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate(services.ModeStandalone)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestAsyncConfig_ApplyDefaults(t *testing.T) {
	tests := []struct {
		name     string
		cfg      LoggingConfig
		expected AsyncConfig
	}{
		{
			name: "zero values get defaults",
			cfg: LoggingConfig{
				Async: AsyncConfig{
					Enabled:      false,
					BufferSize:   0,
					BatchSize:    0,
					FlushTimeout: 0,
				},
			},
			expected: AsyncConfig{
				Enabled:      false,
				BufferSize:   10000,
				BatchSize:    100,
				FlushTimeout: 100,
			},
		},
		{
			name: "custom values preserved",
			cfg: LoggingConfig{
				Async: AsyncConfig{
					Enabled:      true,
					BufferSize:   5000,
					BatchSize:    50,
					FlushTimeout: 50,
				},
			},
			expected: AsyncConfig{
				Enabled:      true,
				BufferSize:   5000,
				BatchSize:    50,
				FlushTimeout: 50,
			},
		},
		{
			name: "partial custom values",
			cfg: LoggingConfig{
				Async: AsyncConfig{
					Enabled:      true,
					BufferSize:   5000,
					BatchSize:    0,
					FlushTimeout: 0,
				},
			},
			expected: AsyncConfig{
				Enabled:      true,
				BufferSize:   5000,
				BatchSize:    100,
				FlushTimeout: 100,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.ApplyDefaults()
			assert.Equal(t, tt.expected, tt.cfg.Async)
		})
	}
}
