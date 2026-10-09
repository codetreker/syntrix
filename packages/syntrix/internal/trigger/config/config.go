package config

import (
	"fmt"
	"os"
	"path/filepath"

	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/codetreker/syntrix/internal/trigger/delivery"
	"github.com/codetreker/syntrix/internal/trigger/evaluator"
)

// Config is the top-level trigger configuration.
type Config struct {
	NatsURL string `yaml:"nats_url"`

	// Evaluator service configuration
	Evaluator evaluator.Config `yaml:"evaluator"`

	// Delivery service configuration
	Delivery delivery.Config `yaml:"delivery"`
}

// DefaultConfig returns the default trigger configuration.
func DefaultConfig() Config {
	return Config{
		NatsURL:   "nats://localhost:4222",
		Evaluator: evaluator.DefaultConfig(),
		Delivery:  delivery.DefaultConfig(),
	}
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.NatsURL == "" {
		c.NatsURL = defaults.NatsURL
	}
	c.Evaluator.ApplyDefaults()
	c.Delivery.ApplyDefaults()
}

// ApplyEnvOverrides applies environment variable overrides.
func (c *Config) ApplyEnvOverrides() {
	if val := os.Getenv("TRIGGER_NATS_URL"); val != "" {
		c.NatsURL = val
	}

	c.Evaluator.ApplyEnvOverrides()
	c.Delivery.ApplyEnvOverrides()
}

// ResolvePaths resolves relative paths using the given directories.
// - configDir: base directory for config-related paths (rules_path)
// - dataDir: not used for trigger config
func (c *Config) ResolvePaths(configDir, dataDir string) {
	if c.Evaluator.RulesPath != "" && !filepath.IsAbs(c.Evaluator.RulesPath) {
		c.Evaluator.RulesPath = filepath.Join(configDir, c.Evaluator.RulesPath)
	}
	c.Evaluator.ResolvePaths(configDir, dataDir)
	c.Delivery.ResolvePaths(configDir, dataDir)
}

// Validate returns an error if the configuration is invalid.
func (c *Config) Validate(mode services.DeploymentMode) error {
	// Delegate to sub-configs; returns nil if all valid
	if err := c.Evaluator.Validate(mode); err != nil {
		return fmt.Errorf("evaluator: %w", err)
	}
	return c.Delivery.Validate(mode)
}
