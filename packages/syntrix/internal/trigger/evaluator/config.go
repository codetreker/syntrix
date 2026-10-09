package evaluator

import (
	"errors"
	"fmt"
	"os"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	services "github.com/codetreker/syntrix/internal/services/config"
)

// Config contains all configuration for the evaluator service.
type Config struct {
	// PullerAddr is the address of the Puller gRPC service.
	// Used in distributed mode to connect to remote Puller.
	// In standalone mode, this is ignored and local Puller is used.
	PullerAddr string `yaml:"puller_addr"`

	// Service behavior
	StartFromNow       bool   `yaml:"start_from_now"`
	RulesPath          string `yaml:"rules_path"`
	CheckpointDatabase string `yaml:"checkpoint_database"`

	// Pubsub configuration
	StreamName    string `yaml:"stream_name"`
	RetryAttempts int    `yaml:"retry_attempts"`
	StorageType   string `yaml:"storage_type"`
}

// DefaultConfig returns default evaluator configuration.
func DefaultConfig() Config {
	return Config{
		PullerAddr:         "localhost:9000",
		StartFromNow:       true,
		RulesPath:          "triggers",
		CheckpointDatabase: "default",
		StreamName:         "TRIGGERS",
		RetryAttempts:      3,
		StorageType:        "file",
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
	if c.PullerAddr == "" {
		c.PullerAddr = defaults.PullerAddr
	}
	if c.CheckpointDatabase == "" {
		c.CheckpointDatabase = defaults.CheckpointDatabase
	}
	if c.StreamName == "" {
		c.StreamName = defaults.StreamName
	}
	if c.RetryAttempts <= 0 {
		c.RetryAttempts = defaults.RetryAttempts
	}
	if c.StorageType == "" {
		c.StorageType = defaults.StorageType
	}
}

// ApplyEnvOverrides applies environment variable overrides.
func (c *Config) ApplyEnvOverrides() {
	if val := os.Getenv("TRIGGER_RULES_PATH"); val != "" {
		c.RulesPath = val
	}
	if val := os.Getenv("TRIGGER_PULLER_ADDR"); val != "" {
		c.PullerAddr = val
	}
}

// ResolvePaths resolves relative paths using the given directories.
// RulesFile is resolved at the parent trigger.Config level.
func (c *Config) ResolvePaths(_, _ string) { _ = c }

// Validate returns an error if the configuration is invalid.
func (c *Config) Validate(mode services.DeploymentMode) error {
	if c.RulesPath == "" {
		return errors.New("trigger.evaluator.rules_path is required")
	}
	if c.StreamName == "" {
		return errors.New("stream_name is required")
	}
	if c.RetryAttempts < 0 {
		return errors.New("retry_attempts must be non-negative")
	}
	if c.StorageType != "" && c.StorageType != "file" && c.StorageType != "memory" {
		return errors.New("storage_type must be 'file' or 'memory'")
	}
	if mode.IsDistributed() && c.PullerAddr == "" {
		return fmt.Errorf("trigger.evaluator.puller_addr is required in distributed mode")
	}
	return nil
}
