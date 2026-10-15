package config

import (
	"os"
	"path/filepath"
	"testing"

	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"
	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/codetreker/syntrix/internal/trigger/delivery"
	"github.com/codetreker/syntrix/internal/trigger/evaluator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, "nats://localhost:4222", cfg.NatsURL)
	require.NotNil(t, cfg.Broker)
	assert.Equal(t, pubsubconfig.DefaultOperatorBinding(), *cfg.Broker)

	// Evaluator config
	assert.Equal(t, "localhost:9000", cfg.Evaluator.PullerAddr)
	assert.Equal(t, "TRIGGERS", cfg.Evaluator.StreamName)
	assert.Equal(t, 3, cfg.Evaluator.RetryAttempts)
	assert.Equal(t, "file", cfg.Evaluator.StorageType)
	assert.True(t, cfg.Evaluator.StartFromNow)
	assert.Equal(t, "default", cfg.Evaluator.CheckpointDatabase)

	// Delivery config
	assert.Equal(t, "TRIGGERS", cfg.Delivery.StreamName)
	assert.Equal(t, "trigger-delivery", cfg.Delivery.ConsumerName)
	assert.Equal(t, "file", cfg.Delivery.StorageType)
	assert.Equal(t, 16, cfg.Delivery.NumWorkers)
}

func TestConfig_StructFields(t *testing.T) {
	cfg := Config{
		NatsURL: "nats://custom:4222",
		Evaluator: evaluator.Config{
			StreamName:         "custom_stream",
			RulesPath:          "custom_triggers",
			CheckpointDatabase: "custom_db",
		},
		Delivery: delivery.Config{
			StreamName:   "custom_stream",
			ConsumerName: "custom-consumer",
			NumWorkers:   8,
		},
	}

	assert.Equal(t, "nats://custom:4222", cfg.NatsURL)
	assert.Equal(t, "custom_stream", cfg.Evaluator.StreamName)
	assert.Equal(t, "custom_triggers", cfg.Evaluator.RulesPath)
	assert.Equal(t, "custom_db", cfg.Evaluator.CheckpointDatabase)
	assert.Equal(t, "custom_stream", cfg.Delivery.StreamName)
	assert.Equal(t, "custom-consumer", cfg.Delivery.ConsumerName)
	assert.Equal(t, 8, cfg.Delivery.NumWorkers)
}

func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()

	assert.Equal(t, "nats://localhost:4222", cfg.NatsURL)
	assert.Equal(t, "TRIGGERS", cfg.Evaluator.StreamName)
	assert.Equal(t, 3, cfg.Evaluator.RetryAttempts)
	assert.Equal(t, 16, cfg.Delivery.NumWorkers)
	assert.Nil(t, cfg.Broker)
}

func TestConfig_BrokerBindingDefaultsAndValidation(t *testing.T) {
	binding := pubsubconfig.DefaultOperatorBinding()
	binding.ResourceMode = "managed"
	binding.StreamName = "SYNTRIX_" + binding.Ownership.InstanceID + "_TRIGGERS"
	binding.SubjectPrefix = binding.StreamName
	binding.ConsumerName = binding.StreamName + "_DELIVERY"
	binding.Policy.Storage = "memory"
	cfg := Config{Broker: &binding, Evaluator: evaluator.Config{RulesPath: "triggers"}}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate(services.ModeDistributed))
	assert.Equal(t, binding.StreamName, cfg.Evaluator.StreamName)
	assert.Equal(t, binding.StreamName, cfg.Delivery.StreamName)
	assert.Equal(t, binding.ConsumerName, cfg.Delivery.ConsumerName)
	assert.Equal(t, "memory", cfg.Evaluator.StorageType)
	assert.Equal(t, "memory", cfg.Delivery.StorageType)

	tests := []struct {
		name    string
		mutate  func(*Config)
		message string
	}{
		{"missing", func(c *Config) { c.Broker = nil }, "broker is required"},
		{"incomplete", func(c *Config) { copy := *c.Broker; copy.SpecRevision = 0; c.Broker = &copy }, "spec_revision"},
		{"evaluator stream", func(c *Config) { c.Evaluator.StreamName = "OTHER" }, "stream_name must match"},
		{"delivery stream", func(c *Config) { c.Delivery.StreamName = "OTHER" }, "stream_name must match"},
		{"delivery durable", func(c *Config) { c.Delivery.ConsumerName = "other" }, "consumer_name must match"},
		{"evaluator storage", func(c *Config) { c.Evaluator.StorageType = "file" }, "storage_type must match"},
		{"delivery storage", func(c *Config) { c.Delivery.StorageType = "file" }, "storage_type must match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := cfg
			tt.mutate(&candidate)
			candidate.ApplyDefaults()
			assert.ErrorContains(t, candidate.Validate(services.ModeDistributed), tt.message)
		})
	}
	cfg.Broker = nil
	assert.NoError(t, cfg.Validate(services.ModeStandalone))
}

func TestConfig_ApplyEnvOverrides(t *testing.T) {
	os.Setenv("TRIGGER_NATS_URL", "nats://env:4222")
	os.Setenv("TRIGGER_RULES_PATH", "env_rules")
	os.Setenv("TRIGGER_PULLER_ADDR", "puller.env:9000")
	defer func() {
		os.Unsetenv("TRIGGER_NATS_URL")
		os.Unsetenv("TRIGGER_RULES_PATH")
		os.Unsetenv("TRIGGER_PULLER_ADDR")
	}()

	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()

	assert.Equal(t, "nats://env:4222", cfg.NatsURL)
	assert.Equal(t, "env_rules", cfg.Evaluator.RulesPath)
	assert.Equal(t, "puller.env:9000", cfg.Evaluator.PullerAddr)
}

func TestConfig_ResolvePaths(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Evaluator.RulesPath = "triggers"
	cfg.ResolvePaths("config", "data")

	assert.Equal(t, filepath.Join("config", "triggers"), cfg.Evaluator.RulesPath)
}

func TestConfig_ResolvePaths_AbsolutePath(t *testing.T) {
	cfg := DefaultConfig()
	absPath := "/absolute/path/triggers"
	cfg.Evaluator.RulesPath = absPath
	cfg.ResolvePaths("config", "data")

	assert.Equal(t, absPath, cfg.Evaluator.RulesPath)
}

func TestConfig_Validate(t *testing.T) {
	cfg := DefaultConfig()
	err := cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}

func TestConfig_ApplyDefaults_CustomValuesPreserved(t *testing.T) {
	cfg := &Config{
		NatsURL: "nats://custom:4223",
		Evaluator: evaluator.Config{
			StreamName:         "CUSTOM_STREAM",
			RulesPath:          "custom_rules",
			CheckpointDatabase: "custom_db",
			RetryAttempts:      5,
			StorageType:        "memory",
		},
		Delivery: delivery.Config{
			StreamName:   "CUSTOM_STREAM",
			ConsumerName: "custom-consumer",
			NumWorkers:   32,
			StorageType:  "memory",
		},
	}
	cfg.ApplyDefaults()

	assert.Equal(t, "nats://custom:4223", cfg.NatsURL)
	assert.Equal(t, "CUSTOM_STREAM", cfg.Evaluator.StreamName)
	assert.Equal(t, "custom_rules", cfg.Evaluator.RulesPath)
	assert.Equal(t, "custom_db", cfg.Evaluator.CheckpointDatabase)
	assert.Equal(t, 5, cfg.Evaluator.RetryAttempts)
	assert.Equal(t, "memory", cfg.Evaluator.StorageType)
	assert.Equal(t, "CUSTOM_STREAM", cfg.Delivery.StreamName)
	assert.Equal(t, "custom-consumer", cfg.Delivery.ConsumerName)
	assert.Equal(t, 32, cfg.Delivery.NumWorkers)
	assert.Equal(t, "memory", cfg.Delivery.StorageType)
}

func TestConfig_ApplyDefaults_PartialConfig(t *testing.T) {
	cfg := &Config{
		NatsURL: "nats://partial:4222",
		// Other fields empty, should get defaults
	}
	cfg.ApplyDefaults()

	assert.Equal(t, "nats://partial:4222", cfg.NatsURL)
	assert.Equal(t, "TRIGGERS", cfg.Evaluator.StreamName)
	assert.Equal(t, 3, cfg.Evaluator.RetryAttempts)
	assert.Equal(t, "default", cfg.Evaluator.CheckpointDatabase)
	assert.Equal(t, 16, cfg.Delivery.NumWorkers)
}

func TestConfig_ApplyEnvOverrides_WithTSetenv(t *testing.T) {
	t.Setenv("TRIGGER_NATS_URL", "nats://testenv:4222")
	t.Setenv("TRIGGER_RULES_PATH", "testenv_rules")

	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()

	assert.Equal(t, "nats://testenv:4222", cfg.NatsURL)
	assert.Equal(t, "testenv_rules", cfg.Evaluator.RulesPath)
}

func TestConfig_ApplyEnvOverrides_NoEnvVars(t *testing.T) {
	cfg := DefaultConfig()
	originalURL := cfg.NatsURL

	cfg.ApplyEnvOverrides()

	assert.Equal(t, originalURL, cfg.NatsURL)
}

func TestConfig_ResolvePaths_EmptyRulesPath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Evaluator.RulesPath = ""
	cfg.ResolvePaths("/config", "/data")

	// Empty path should stay empty (not get resolved to just baseDir)
	assert.Equal(t, "", cfg.Evaluator.RulesPath)
}

func TestConfig_Validate_EmptyConfig(t *testing.T) {
	// Empty config is now invalid because RulesPath is required
	cfg := Config{}
	err := cfg.Validate(services.ModeDistributed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rules_path is required")
}

func TestConfig_Validate_CallsSubConfigs(t *testing.T) {
	// Verify that Validate calls sub-config Validate methods
	cfg := DefaultConfig()
	err := cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}

func TestConfig_Validate_EvaluatorError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Evaluator.StreamName = "" // This triggers evaluator validation error
	err := cfg.Validate(services.ModeDistributed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "evaluator")
}

func TestConfig_Validate_DeliveryError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Delivery.StreamName = "" // This triggers delivery validation error
	err := cfg.Validate(services.ModeDistributed)
	assert.Error(t, err)
	// Note: The error goes through evaluator first (which passes), then delivery
	assert.Contains(t, err.Error(), "stream_name is required")
}
