package streamer

import (
	"testing"
	"time"

	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// Verify Service defaults
	assert.Equal(t, DefaultServiceConfig(), cfg.Server)

	// Verify Client defaults
	assert.Equal(t, DefaultClientConfig(), cfg.Client)
}

func TestConfig_StructFields(t *testing.T) {
	cfg := Config{
		Server: ServerConfig{
			PullerAddr:  "localhost:50053",
			SendTimeout: 10 * time.Second,
		},
		Client: ClientConfig{
			StreamerAddr: "custom:50052",
		},
	}

	assert.Equal(t, "localhost:50053", cfg.Server.PullerAddr)
	assert.Equal(t, 10*time.Second, cfg.Server.SendTimeout)
	assert.Equal(t, "custom:50052", cfg.Client.StreamerAddr)
}

func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()

	assert.Equal(t, "http://localhost:9000", cfg.Server.PullerAddr)
	assert.Equal(t, 5*time.Second, cfg.Server.SendTimeout)
	assert.Equal(t, "localhost:50052", cfg.Client.StreamerAddr)
	assert.Equal(t, 1*time.Second, cfg.Client.InitialBackoff)
	assert.Equal(t, 30*time.Second, cfg.Client.MaxBackoff)
	assert.Equal(t, 2.0, cfg.Client.BackoffMultiplier)
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
	err := cfg.Validate(services.ModeStandalone)
	assert.NoError(t, err)
}

func TestConfig_ApplyDefaults_CustomValuesPreserved(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{
			PullerAddr:  "custom:9001",
			SendTimeout: 10 * time.Second,
		},
		Client: ClientConfig{
			StreamerAddr:      "custom:50053",
			InitialBackoff:    2 * time.Second,
			MaxBackoff:        60 * time.Second,
			BackoffMultiplier: 1.5,
			HeartbeatInterval: 60 * time.Second,
			ActivityTimeout:   180 * time.Second,
		},
	}
	cfg.ApplyDefaults()

	assert.Equal(t, "custom:9001", cfg.Server.PullerAddr)
	assert.Equal(t, 10*time.Second, cfg.Server.SendTimeout)
	assert.Equal(t, "custom:50053", cfg.Client.StreamerAddr)
	assert.Equal(t, 2*time.Second, cfg.Client.InitialBackoff)
	assert.Equal(t, 60*time.Second, cfg.Client.MaxBackoff)
	assert.Equal(t, 1.5, cfg.Client.BackoffMultiplier)
	assert.Equal(t, 60*time.Second, cfg.Client.HeartbeatInterval)
	assert.Equal(t, 180*time.Second, cfg.Client.ActivityTimeout)
}

func TestConfig_Validate_DistributedMode(t *testing.T) {
	// In distributed mode, PullerAddr is required
	cfg := &Config{Server: ServerConfig{PullerAddr: ""}}
	err := cfg.Validate(services.ModeDistributed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "streamer.server.puller_addr is required in distributed mode")

	// With PullerAddr set, should pass
	cfg.Server.PullerAddr = "puller:9000"
	err = cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}
