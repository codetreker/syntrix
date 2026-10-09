// Package config provides configuration for the Query service.
package config

import (
	"fmt"

	services "github.com/codetreker/syntrix/internal/services/config"
)

// Config holds the Query service configuration.
type Config struct {
	// IndexerAddr is the address of the Indexer gRPC service.
	// Used in distributed mode to connect to Indexer.
	// Defaults to "localhost:9000".
	IndexerAddr string `yaml:"indexer_addr"`
}

// DefaultConfig returns the default Query configuration.
func DefaultConfig() Config {
	return Config{
		IndexerAddr: "localhost:9000",
	}
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.IndexerAddr == "" {
		c.IndexerAddr = defaults.IndexerAddr
	}
}

// ApplyEnvOverrides applies environment variable overrides.
// No env vars for query config currently.
func (c *Config) ApplyEnvOverrides() { _ = c }

// ResolvePaths resolves relative paths using the given directories.
// No paths to resolve in query config.
func (c *Config) ResolvePaths(_, _ string) { _ = c }

// Validate returns an error if the configuration is invalid.
func (c *Config) Validate(mode services.DeploymentMode) error {
	if mode.IsDistributed() && c.IndexerAddr == "" {
		return fmt.Errorf("query.indexer_addr is required in distributed mode")
	}
	return nil
}
