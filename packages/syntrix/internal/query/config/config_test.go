package config

import (
	"testing"

	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, "localhost:9000", cfg.IndexerAddr)
}

func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()

	assert.Equal(t, "localhost:9000", cfg.IndexerAddr)
}

func TestConfig_ApplyEnvOverrides(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()
	// No env vars, just verify no panic
}

func TestConfig_ResolvePaths(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ResolvePaths("config", "data")
	// No paths to resolve, just verify no panic
}

func TestConfig_Validate(t *testing.T) {
	cfg := DefaultConfig()
	err := cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}

func TestConfig_ApplyDefaults_CustomValuesPreserved(t *testing.T) {
	cfg := &Config{
		IndexerAddr: "custom:9001",
	}
	cfg.ApplyDefaults()

	assert.Equal(t, "custom:9001", cfg.IndexerAddr)
}

func TestConfig_Validate_EmptyConfig(t *testing.T) {
	cfg := Config{}
	err := cfg.Validate(services.ModeStandalone)
	assert.NoError(t, err)
}

func TestConfig_StructFields(t *testing.T) {
	cfg := Config{
		IndexerAddr: "custom-indexer:9002",
	}

	assert.Equal(t, "custom-indexer:9002", cfg.IndexerAddr)
}

func TestConfig_Validate_DistributedMode(t *testing.T) {
	// In distributed mode, IndexerAddr is required
	cfg := &Config{IndexerAddr: ""}
	err := cfg.Validate(services.ModeDistributed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "query.indexer_addr is required in distributed mode")

	// With IndexerAddr set, should pass
	cfg.IndexerAddr = "indexer:9000"
	err = cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}
