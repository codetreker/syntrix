package delivery

import (
	"errors"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	services "github.com/codetreker/syntrix/internal/services/config"
)

// Config contains all configuration for the delivery service.
type Config struct {
	// Service behavior
	NumWorkers      int           `yaml:"num_workers"`
	ChannelBufSize  int           `yaml:"channel_buf_size"`
	DrainTimeout    time.Duration `yaml:"drain_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`

	// Pubsub configuration
	StreamName   string `yaml:"stream_name"`
	ConsumerName string `yaml:"consumer_name"`
	StorageType  string `yaml:"storage_type"`
}

// DefaultConfig returns default delivery configuration.
func DefaultConfig() Config {
	return Config{
		NumWorkers:      16,
		ChannelBufSize:  0,
		DrainTimeout:    0,
		ShutdownTimeout: 0,
		StreamName:      "TRIGGERS",
		ConsumerName:    "trigger-delivery",
		StorageType:     "file",
	}
}

// StorageTypeValue returns the pubsub.StorageType from the config string.
func (c Config) StorageTypeValue() pubsub.StorageType {
	if c.StorageType == "memory" {
		return pubsub.MemoryStorage
	}
	return pubsub.FileStorage
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.NumWorkers <= 0 {
		c.NumWorkers = defaults.NumWorkers
	}
	if c.StreamName == "" {
		c.StreamName = defaults.StreamName
	}
	if c.ConsumerName == "" {
		c.ConsumerName = defaults.ConsumerName
	}
	if c.StorageType == "" {
		c.StorageType = defaults.StorageType
	}
}

// ApplyEnvOverrides applies environment variable overrides.
// No delivery-specific env vars.
func (c *Config) ApplyEnvOverrides() { _ = c }

// ResolvePaths resolves relative paths using the given directories.
// No paths to resolve in delivery config.
func (c *Config) ResolvePaths(_, _ string) { _ = c }

// Validate returns an error if the configuration is invalid.
func (c *Config) Validate(_ services.DeploymentMode) error {
	if c.NumWorkers < 0 {
		return errors.New("num_workers must be non-negative")
	}
	if c.StreamName == "" {
		return errors.New("stream_name is required")
	}
	if c.ConsumerName == "" {
		return errors.New("consumer_name is required")
	}
	if c.StorageType != "" && c.StorageType != "file" && c.StorageType != "memory" {
		return errors.New("storage_type must be 'file' or 'memory'")
	}
	return nil
}
