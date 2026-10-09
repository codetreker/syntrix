package database

import (
	"os"
	"strconv"
	"time"
)

// Config contains database management configuration
type Config struct {
	// MaxDatabasesPerUser limits how many databases a user can create (0 = unlimited)
	MaxDatabasesPerUser int `yaml:"max_databases_per_user"`

	// Cache contains cache configuration
	Cache CacheConfigSettings `yaml:"cache"`

	// Deletion contains deletion worker configuration
	Deletion DeletionConfig `yaml:"deletion"`
}

// CacheConfigSettings contains cache settings for YAML configuration
type CacheConfigSettings struct {
	// Enabled enables caching
	Enabled bool `yaml:"enabled"`
	// TTL is the cache TTL for positive lookups
	TTL time.Duration `yaml:"ttl"`
	// NegativeTTL is the cache TTL for negative (not found) lookups
	NegativeTTL time.Duration `yaml:"negative_ttl"`
	// Size is the maximum number of cached entries
	Size int `yaml:"size"`
}

// DeletionConfig contains deletion worker settings
type DeletionConfig struct {
	// Enabled enables the deletion worker
	Enabled bool `yaml:"enabled"`
	// Interval between cleanup cycles
	Interval time.Duration `yaml:"interval"`
	// BatchSize is the number of documents to delete per batch
	BatchSize int `yaml:"batch_size"`
	// MaxBatchesPerCycle limits batches per database per cycle
	MaxBatchesPerCycle int `yaml:"max_batches_per_cycle"`
}

// DefaultConfig returns the default database configuration
func DefaultConfig() Config {
	return Config{
		MaxDatabasesPerUser: 10,
		Cache: CacheConfigSettings{
			Enabled:     true,
			TTL:         5 * time.Minute,
			NegativeTTL: 1 * time.Minute,
			Size:        1000,
		},
		Deletion: DeletionConfig{
			Enabled:            true,
			Interval:           5 * time.Minute,
			BatchSize:          100,
			MaxBatchesPerCycle: 10,
		},
	}
}

// ToCacheConfig converts settings to internal CacheConfig
func (c *CacheConfigSettings) ToCacheConfig() CacheConfig {
	return CacheConfig{
		Size:        c.Size,
		TTL:         c.TTL,
		NegativeTTL: c.NegativeTTL,
	}
}

// ApplyEnvOverrides applies environment variable overrides
func (c *Config) ApplyEnvOverrides() {
	if v := os.Getenv("DATABASE_MAX_PER_USER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MaxDatabasesPerUser = n
		}
	}
	if v := os.Getenv("DATABASE_DELETION_ENABLED"); v != "" {
		c.Deletion.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("DATABASE_DELETION_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Deletion.Interval = d
		}
	}
}

// ToDeletionWorkerConfig converts to DeletionWorkerConfig
func (c *DeletionConfig) ToDeletionWorkerConfig() DeletionWorkerConfig {
	return DeletionWorkerConfig{
		Interval:           c.Interval,
		BatchSize:          c.BatchSize,
		MaxBatchesPerCycle: c.MaxBatchesPerCycle,
	}
}
