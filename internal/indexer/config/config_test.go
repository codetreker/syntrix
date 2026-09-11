package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	services "github.com/syntrixbase/syntrix/internal/services/config"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, "index_templates", cfg.TemplatePath)
	assert.Equal(t, "data/indexer/progress", cfg.ProgressPath)
	assert.Equal(t, "indexer", cfg.ConsumerID)
	assert.Equal(t, 5*time.Second, cfg.ReconcileInterval)
}

func TestConfig_Fields(t *testing.T) {
	cfg := Config{
		TemplatePath:      "/custom/path/templates.yaml",
		ProgressPath:      "/data/progress",
		ConsumerID:        "custom-indexer",
		ReconcileInterval: 10 * time.Second,
	}

	assert.Equal(t, "/custom/path/templates.yaml", cfg.TemplatePath)
	assert.Equal(t, "/data/progress", cfg.ProgressPath)
	assert.Equal(t, "custom-indexer", cfg.ConsumerID)
	assert.Equal(t, 10*time.Second, cfg.ReconcileInterval)
}

func TestDefaultStoreConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultStoreConfig()

	if cfg.Path != "data/indexer/indexes.db" {
		t.Errorf("Path = %q, want data/indexer/indexes.db", cfg.Path)
	}
	if cfg.BatchSize != 100 {
		t.Errorf("BatchSize = %d, want 100", cfg.BatchSize)
	}
	if cfg.BatchInterval != 100*time.Millisecond {
		t.Errorf("BatchInterval = %v, want 100ms", cfg.BatchInterval)
	}
	if cfg.QueueSize != 10000 {
		t.Errorf("QueueSize = %d, want 10000", cfg.QueueSize)
	}
	if cfg.BlockCacheSize != 128*1024*1024 {
		t.Errorf("BlockCacheSize = %d, want %d", cfg.BlockCacheSize, 128*1024*1024)
	}
}

func TestConfig_ApplyDefaults(t *testing.T) {
	tests := []struct {
		name    string
		initial Config
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name:    "empty config gets all defaults",
			initial: Config{},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "localhost:9000", cfg.PullerAddr)
				assert.Equal(t, "index_templates", cfg.TemplatePath)
				assert.Equal(t, "data/indexer/progress", cfg.ProgressPath)
				assert.Equal(t, "indexer", cfg.ConsumerID)
				assert.Equal(t, 5*time.Second, cfg.ReconcileInterval)
				assert.Equal(t, StorageModeMemory, cfg.StorageMode)
				assert.Equal(t, "data/indexer/indexes.db", cfg.Store.Path)
				assert.Equal(t, 100, cfg.Store.BatchSize)
				assert.Equal(t, 100*time.Millisecond, cfg.Store.BatchInterval)
				assert.Equal(t, 10000, cfg.Store.QueueSize)
				assert.Equal(t, int64(128*1024*1024), cfg.Store.BlockCacheSize)
			},
		},
		{
			name: "custom values preserved",
			initial: Config{
				PullerAddr:        "custom:9001",
				TemplatePath:      "/custom/templates.yaml",
				ProgressPath:      "/custom/progress",
				ConsumerID:        "custom-indexer",
				ReconcileInterval: 10 * time.Second,
				StorageMode:       StorageModePebble,
				Store: StoreConfig{
					Path:           "/custom/store.db",
					BatchSize:      200,
					BatchInterval:  200 * time.Millisecond,
					QueueSize:      20000,
					BlockCacheSize: 256 * 1024 * 1024,
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "custom:9001", cfg.PullerAddr)
				assert.Equal(t, "/custom/templates.yaml", cfg.TemplatePath)
				assert.Equal(t, "/custom/progress", cfg.ProgressPath)
				assert.Equal(t, "custom-indexer", cfg.ConsumerID)
				assert.Equal(t, 10*time.Second, cfg.ReconcileInterval)
				assert.Equal(t, StorageModePebble, cfg.StorageMode)
				assert.Equal(t, "/custom/store.db", cfg.Store.Path)
				assert.Equal(t, 200, cfg.Store.BatchSize)
				assert.Equal(t, 200*time.Millisecond, cfg.Store.BatchInterval)
				assert.Equal(t, 20000, cfg.Store.QueueSize)
				assert.Equal(t, int64(256*1024*1024), cfg.Store.BlockCacheSize)
			},
		},
		{
			name: "partial config gets remaining defaults",
			initial: Config{
				PullerAddr: "partial:9002",
				// Other fields empty, should get defaults
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "partial:9002", cfg.PullerAddr)
				assert.Equal(t, "index_templates", cfg.TemplatePath)
				assert.Equal(t, "data/indexer/progress", cfg.ProgressPath)
				assert.Equal(t, "indexer", cfg.ConsumerID)
				assert.Equal(t, 5*time.Second, cfg.ReconcileInterval)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.initial
			cfg.ApplyDefaults()
			tt.check(t, &cfg)
		})
	}
}

func TestConfig_ApplyEnvOverrides(t *testing.T) {
	// ApplyEnvOverrides is a no-op for indexer config
	cfg := DefaultConfig()
	originalPullerAddr := cfg.PullerAddr

	cfg.ApplyEnvOverrides()

	assert.Equal(t, originalPullerAddr, cfg.PullerAddr)
}

func TestConfig_ResolvePaths(t *testing.T) {
	tests := []struct {
		name              string
		configDir         string
		dataDir           string
		templatePath      string
		progressPath      string
		storePath         string
		expectedTemplate  string
		expectedProgress  string
		expectedStorePath string
	}{
		{
			name:              "relative paths get resolved",
			configDir:         "/config",
			dataDir:           "/app/data",
			templatePath:      "templates.yaml",
			progressPath:      "indexer/progress",
			storePath:         "indexer/indexes.db",
			expectedTemplate:  "/config/templates.yaml",
			expectedProgress:  "/app/data/indexer/progress",
			expectedStorePath: "/app/data/indexer/indexes.db",
		},
		{
			name:              "absolute paths preserved",
			configDir:         "/config",
			dataDir:           "/app/data",
			templatePath:      "/absolute/templates.yaml",
			progressPath:      "/absolute/progress",
			storePath:         "/absolute/indexes.db",
			expectedTemplate:  "/absolute/templates.yaml",
			expectedProgress:  "/absolute/progress",
			expectedStorePath: "/absolute/indexes.db",
		},
		{
			name:              "nested relative paths",
			configDir:         "/app/config",
			dataDir:           "/var/lib/syntrix",
			templatePath:      "sub/templates.yaml",
			progressPath:      "indexer/sub/progress",
			storePath:         "indexer/sub/indexes.db",
			expectedTemplate:  "/app/config/sub/templates.yaml",
			expectedProgress:  "/var/lib/syntrix/indexer/sub/progress",
			expectedStorePath: "/var/lib/syntrix/indexer/sub/indexes.db",
		},
		{
			name:              "empty paths stay empty",
			configDir:         "/config",
			dataDir:           "/app/data",
			templatePath:      "",
			progressPath:      "",
			storePath:         "",
			expectedTemplate:  "",
			expectedProgress:  "",
			expectedStorePath: "",
		},
		{
			name:              "mixed relative and absolute",
			configDir:         "/config",
			dataDir:           "/app/data",
			templatePath:      "templates.yaml",
			progressPath:      "/absolute/progress",
			storePath:         "indexer/indexes.db",
			expectedTemplate:  "/config/templates.yaml",
			expectedProgress:  "/absolute/progress",
			expectedStorePath: "/app/data/indexer/indexes.db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				TemplatePath: tt.templatePath,
				ProgressPath: tt.progressPath,
				Store: StoreConfig{
					Path: tt.storePath,
				},
			}
			cfg.ResolvePaths(tt.configDir, tt.dataDir)
			assert.Equal(t, tt.expectedTemplate, cfg.TemplatePath)
			assert.Equal(t, tt.expectedProgress, cfg.ProgressPath)
			assert.Equal(t, tt.expectedStorePath, cfg.Store.Path)
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	// Validate is currently a no-op that returns nil
	cfg := Config{}
	err := cfg.Validate(services.ModeStandalone)
	assert.NoError(t, err)

	cfg2 := DefaultConfig()
	err = cfg2.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}

func TestConfig_Validate_DistributedMode(t *testing.T) {
	// In distributed mode, PullerAddr is required
	cfg := &Config{PullerAddr: ""}
	err := cfg.Validate(services.ModeDistributed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "indexer.puller_addr is required in distributed mode")

	// With PullerAddr set, should pass
	cfg.PullerAddr = "puller:9000"
	err = cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}

func TestMaintenanceConfigBounds(t *testing.T) {
	defaults := Config{}
	defaults.ApplyDefaults()
	assert.Equal(t, 30*time.Second, defaults.StartupTimeout)
	assert.Equal(t, 256, defaults.BootstrapBatchSize)
	assert.Equal(t, int64(8<<20), defaults.BootstrapPageBytes)
	for _, cfg := range []Config{{StartupTimeout: -time.Second}, {BootstrapBatchSize: -1}, {BootstrapPageBytes: -1}, {Databases: []string{""}}} {
		assert.Error(t, cfg.Validate(services.ModeStandalone))
	}
}
