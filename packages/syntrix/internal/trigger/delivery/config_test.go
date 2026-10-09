package delivery

import (
	"testing"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
)

func TestConfig_StorageTypeValue(t *testing.T) {
	tests := []struct {
		name        string
		storageType string
		expected    pubsub.StorageType
	}{
		{
			name:        "memory storage",
			storageType: "memory",
			expected:    pubsub.MemoryStorage,
		},
		{
			name:        "file storage",
			storageType: "file",
			expected:    pubsub.FileStorage,
		},
		{
			name:        "empty defaults to file",
			storageType: "",
			expected:    pubsub.FileStorage,
		},
		{
			name:        "unknown defaults to file",
			storageType: "unknown",
			expected:    pubsub.FileStorage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{StorageType: tt.storageType}
			assert.Equal(t, tt.expected, cfg.StorageTypeValue())
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, 16, cfg.NumWorkers)
	assert.Equal(t, 0, cfg.ChannelBufSize)
	assert.Equal(t, "TRIGGERS", cfg.StreamName)
	assert.Equal(t, "trigger-delivery", cfg.ConsumerName)
	assert.Equal(t, "file", cfg.StorageType)
}

func TestConfig_ApplyDefaults(t *testing.T) {
	// Empty config should get all defaults
	cfg := Config{}
	cfg.ApplyDefaults()

	assert.Equal(t, 16, cfg.NumWorkers)
	assert.Equal(t, "TRIGGERS", cfg.StreamName)
	assert.Equal(t, "trigger-delivery", cfg.ConsumerName)
	assert.Equal(t, "file", cfg.StorageType)

	// Custom values should be preserved
	cfg2 := Config{
		NumWorkers:   8,
		StreamName:   "CUSTOM_STREAM",
		ConsumerName: "custom-consumer",
		StorageType:  "memory",
	}
	cfg2.ApplyDefaults()

	assert.Equal(t, 8, cfg2.NumWorkers)
	assert.Equal(t, "CUSTOM_STREAM", cfg2.StreamName)
	assert.Equal(t, "custom-consumer", cfg2.ConsumerName)
	assert.Equal(t, "memory", cfg2.StorageType)
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

func TestNewConsumer_NilConnection(t *testing.T) {
	cfg := Config{}

	// Test with nil connection - should fail to create JetStream
	consumer, err := NewConsumer(nil, cfg)
	assert.Error(t, err)
	assert.Nil(t, consumer)
	assert.Contains(t, err.Error(), "failed to create JetStream")
}

func TestConfig_ApplyDefaults_PartialConfig(t *testing.T) {
	cfg := Config{
		NumWorkers:   32,
		ConsumerName: "my-consumer",
		// StreamName empty, should get default
	}
	cfg.ApplyDefaults()

	assert.Equal(t, 32, cfg.NumWorkers)
	assert.Equal(t, "my-consumer", cfg.ConsumerName)
	assert.Equal(t, "TRIGGERS", cfg.StreamName)
	assert.Equal(t, "file", cfg.StorageType)
}

func TestConfig_Validate_EmptyConfig(t *testing.T) {
	cfg := Config{}
	err := cfg.Validate(services.ModeDistributed)
	// Empty StreamName should fail validation
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stream_name is required")
}

func TestConfig_Validate_Errors(t *testing.T) {
	tests := []struct {
		name   string
		cfg    Config
		errMsg string
	}{
		{
			name:   "negative num workers",
			cfg:    Config{NumWorkers: -1, StreamName: "TEST", ConsumerName: "test"},
			errMsg: "num_workers must be non-negative",
		},
		{
			name:   "empty stream name",
			cfg:    Config{StreamName: "", ConsumerName: "test"},
			errMsg: "stream_name is required",
		},
		{
			name:   "empty consumer name",
			cfg:    Config{StreamName: "TEST", ConsumerName: ""},
			errMsg: "consumer_name is required",
		},
		{
			name:   "invalid storage type",
			cfg:    Config{StreamName: "TEST", ConsumerName: "test", StorageType: "invalid"},
			errMsg: "storage_type must be 'file' or 'memory'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate(services.ModeDistributed)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

func TestConfig_Validate_ValidConfigs(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "default config",
			cfg:  DefaultConfig(),
		},
		{
			name: "memory storage",
			cfg:  Config{StreamName: "TEST", ConsumerName: "test", StorageType: "memory"},
		},
		{
			name: "file storage",
			cfg:  Config{StreamName: "TEST", ConsumerName: "test", StorageType: "file"},
		},
		{
			name: "empty storage type (defaults allowed)",
			cfg:  Config{StreamName: "TEST", ConsumerName: "test", StorageType: ""},
		},
		{
			name: "zero num workers",
			cfg:  Config{StreamName: "TEST", ConsumerName: "test", NumWorkers: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate(services.ModeDistributed)
			assert.NoError(t, err)
		})
	}
}

func TestConfig_StructFields(t *testing.T) {
	cfg := Config{
		NumWorkers:     64,
		ChannelBufSize: 100,
		StreamName:     "CUSTOM_STREAM",
		ConsumerName:   "custom-consumer",
		StorageType:    "memory",
	}

	assert.Equal(t, 64, cfg.NumWorkers)
	assert.Equal(t, 100, cfg.ChannelBufSize)
	assert.Equal(t, "CUSTOM_STREAM", cfg.StreamName)
	assert.Equal(t, "custom-consumer", cfg.ConsumerName)
	assert.Equal(t, "memory", cfg.StorageType)
}
