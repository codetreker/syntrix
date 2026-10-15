package config

import (
	"fmt"
	"os"
	"path/filepath"

	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"
	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/codetreker/syntrix/internal/trigger/delivery"
	"github.com/codetreker/syntrix/internal/trigger/evaluator"
)

// Config is the top-level trigger configuration.
type Config struct {
	NatsURL string                      `yaml:"nats_url"`
	Broker  *pubsubconfig.BrokerBinding `yaml:"broker"`

	// Evaluator service configuration
	Evaluator evaluator.Config `yaml:"evaluator"`

	// Delivery service configuration
	Delivery delivery.Config `yaml:"delivery"`
}

// DefaultConfig returns the default trigger configuration.
func DefaultConfig() Config {
	broker := pubsubconfig.DefaultOperatorBinding()
	cfg := Config{
		NatsURL:   "nats://localhost:4222",
		Broker:    &broker,
		Evaluator: evaluator.DefaultConfig(),
		Delivery:  delivery.DefaultConfig(),
	}
	cfg.Evaluator.StreamName = broker.StreamName
	cfg.Evaluator.StorageType = broker.Policy.Storage
	cfg.Delivery.StreamName = broker.StreamName
	cfg.Delivery.ConsumerName = broker.ConsumerName
	cfg.Delivery.StorageType = broker.Policy.Storage
	return cfg
}

// PrepareForLoad leaves file-owned broker authority unset until loading is complete.
// Routing defaults are derived from that binding after all files are decoded.
func (c *Config) PrepareForLoad() {
	c.Broker = nil
	c.Evaluator.StreamName = ""
	c.Evaluator.StorageType = ""
	c.Delivery.StreamName = ""
	c.Delivery.ConsumerName = ""
	c.Delivery.StorageType = ""
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.NatsURL == "" {
		c.NatsURL = defaults.NatsURL
	}
	if c.Broker != nil {
		if c.Evaluator.StreamName == "" {
			c.Evaluator.StreamName = c.Broker.StreamName
		}
		if c.Evaluator.StorageType == "" {
			c.Evaluator.StorageType = c.Broker.Policy.Storage
		}
		if c.Delivery.StreamName == "" {
			c.Delivery.StreamName = c.Broker.StreamName
		}
		if c.Delivery.ConsumerName == "" {
			c.Delivery.ConsumerName = c.Broker.ConsumerName
		}
		if c.Delivery.StorageType == "" {
			c.Delivery.StorageType = c.Broker.Policy.Storage
		}
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
	if err := c.Evaluator.Validate(mode); err != nil {
		return fmt.Errorf("evaluator: %w", err)
	}
	if err := c.Delivery.Validate(mode); err != nil {
		return fmt.Errorf("delivery: %w", err)
	}
	if !mode.IsDistributed() {
		return nil
	}
	if c.Broker == nil {
		return fmt.Errorf("broker is required in distributed mode")
	}
	if _, err := c.Broker.Specification(); err != nil {
		return fmt.Errorf("broker: %w", err)
	}
	if c.Evaluator.StreamName != c.Broker.StreamName || c.Delivery.StreamName != c.Broker.StreamName {
		return fmt.Errorf("evaluator and delivery stream_name must match broker stream_name")
	}
	if c.Delivery.ConsumerName != c.Broker.ConsumerName {
		return fmt.Errorf("delivery consumer_name must match broker consumer_name")
	}
	if c.Evaluator.StorageType != c.Broker.Policy.Storage || c.Delivery.StorageType != c.Broker.Policy.Storage {
		return fmt.Errorf("evaluator and delivery storage_type must match broker policy storage")
	}
	return nil
}
