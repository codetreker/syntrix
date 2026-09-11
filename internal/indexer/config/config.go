// Package config provides configuration for the Indexer service.
package config

import (
	"fmt"
	"path/filepath"
	"time"

	services "github.com/syntrixbase/syntrix/internal/services/config"
)

type StorageMode string

const (
	StorageModeMemory StorageMode = "memory"
	StorageModePebble StorageMode = "pebble"
)

// Config holds the Indexer service configuration.
type Config struct {
	// Databases requires complete bootstrap coverage for these logical database IDs.
	Databases          []string      `yaml:"databases"`
	StartupTimeout     time.Duration `yaml:"startup_timeout"`
	BootstrapBatchSize int           `yaml:"bootstrap_batch_size"`
	BootstrapPageBytes int64         `yaml:"bootstrap_page_bytes"`

	// PullerAddr is the address of the Puller gRPC service.
	// Used in distributed mode to connect to Puller.
	// Defaults to "localhost:9000".
	PullerAddr string `yaml:"puller_addr"`

	// TemplatePath is the path to the index templates YAML file.
	TemplatePath string `yaml:"template_path"`

	// ProgressPath is the path to store progress markers.
	// Defaults to "data/indexer/progress".
	ProgressPath string `yaml:"progress_path"`

	// ConsumerID is the ID used when subscribing to Puller.
	// Defaults to "indexer".
	ConsumerID string `yaml:"consumer_id"`

	// ReconcileInterval is the interval between reconciliation loops.
	// Defaults to 5s.
	ReconcileInterval time.Duration `yaml:"reconcile_interval"`

	// StorageMode selects the storage backend: "memory" or "pebble".
	// Defaults to "memory".
	StorageMode StorageMode `yaml:"storage_mode"`

	// Store configures the PebbleDB storage backend.
	// Only used when StorageMode is "pebble".
	Store StoreConfig `yaml:"store"`
}

// StoreConfig holds the configuration for PebbleDB storage.
type StoreConfig struct {
	// Path is the directory to store the database.
	// Defaults to "data/indexer/indexes.db".
	Path string `yaml:"path"`

	// BatchSize is the max number of operations per batch.
	// Defaults to 100.
	BatchSize int `yaml:"batch_size"`

	// BatchInterval is the max time to wait before flushing a batch.
	// Defaults to 100ms.
	BatchInterval time.Duration `yaml:"batch_interval"`

	// QueueSize is the buffer for pending writes.
	// Defaults to 10000.
	QueueSize int `yaml:"queue_size"`

	// BlockCacheSize is the size of the block cache in bytes.
	// Defaults to 64MB.
	BlockCacheSize int64 `yaml:"block_cache_size"`
}

// DefaultConfig returns the default Indexer configuration.
func DefaultConfig() Config {
	return Config{
		PullerAddr:         "localhost:9000",
		StartupTimeout:     30 * time.Second,
		BootstrapBatchSize: 256,
		BootstrapPageBytes: 8 << 20,
		TemplatePath:       "index_templates",
		ProgressPath:       "data/indexer/progress",
		ConsumerID:         "indexer",
		ReconcileInterval:  5 * time.Second,
		StorageMode:        StorageModeMemory,
		Store:              DefaultStoreConfig(),
	}
}

// DefaultStoreConfig returns the default StoreConfig.
func DefaultStoreConfig() StoreConfig {
	return StoreConfig{
		Path:           "data/indexer/indexes.db",
		BatchSize:      100,
		BatchInterval:  100 * time.Millisecond,
		QueueSize:      10000,
		BlockCacheSize: 128 * 1024 * 1024, // 128MB
	}
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.StartupTimeout == 0 {
		c.StartupTimeout = defaults.StartupTimeout
	}
	if c.BootstrapBatchSize == 0 {
		c.BootstrapBatchSize = defaults.BootstrapBatchSize
	}
	if c.BootstrapPageBytes == 0 {
		c.BootstrapPageBytes = defaults.BootstrapPageBytes
	}
	if c.PullerAddr == "" {
		c.PullerAddr = defaults.PullerAddr
	}
	if c.TemplatePath == "" {
		c.TemplatePath = defaults.TemplatePath
	}
	if c.ProgressPath == "" {
		c.ProgressPath = defaults.ProgressPath
	}
	if c.ConsumerID == "" {
		c.ConsumerID = defaults.ConsumerID
	}
	if c.ReconcileInterval == 0 {
		c.ReconcileInterval = defaults.ReconcileInterval
	}
	if c.StorageMode == "" {
		c.StorageMode = defaults.StorageMode
	}
	// Apply store defaults
	storeDefaults := DefaultStoreConfig()
	if c.Store.Path == "" {
		c.Store.Path = storeDefaults.Path
	}
	if c.Store.BatchSize == 0 {
		c.Store.BatchSize = storeDefaults.BatchSize
	}
	if c.Store.BatchInterval == 0 {
		c.Store.BatchInterval = storeDefaults.BatchInterval
	}
	if c.Store.QueueSize == 0 {
		c.Store.QueueSize = storeDefaults.QueueSize
	}
	if c.Store.BlockCacheSize == 0 {
		c.Store.BlockCacheSize = storeDefaults.BlockCacheSize
	}
}

// ApplyEnvOverrides applies environment variable overrides.
// No env vars for indexer config currently.
func (c *Config) ApplyEnvOverrides() { _ = c }

// ResolvePaths resolves relative paths using the given directories.
// - configDir: base directory for config-related paths (template_path)
// - dataDir: base directory for runtime data paths (progress_path, store.path)
func (c *Config) ResolvePaths(configDir, dataDir string) {
	// Config-related paths
	if c.TemplatePath != "" && !filepath.IsAbs(c.TemplatePath) {
		c.TemplatePath = filepath.Join(configDir, c.TemplatePath)
	}
	// Data-related paths
	if c.ProgressPath != "" && !filepath.IsAbs(c.ProgressPath) {
		c.ProgressPath = filepath.Join(dataDir, c.ProgressPath)
	}
	if c.Store.Path != "" && !filepath.IsAbs(c.Store.Path) {
		c.Store.Path = filepath.Join(dataDir, c.Store.Path)
	}
}

// Validate returns an error if the configuration is invalid.
func (c *Config) Validate(mode services.DeploymentMode) error {
	if c.StartupTimeout < 0 || c.BootstrapBatchSize < 0 || c.BootstrapPageBytes < 0 {
		return fmt.Errorf("indexer maintenance limits must be positive")
	}
	for _, database := range c.Databases {
		if database == "" {
			return fmt.Errorf("indexer.databases cannot contain an empty database ID")
		}
	}

	if mode.IsDistributed() && c.PullerAddr == "" {
		return fmt.Errorf("indexer.puller_addr is required in distributed mode")
	}
	return nil
}
