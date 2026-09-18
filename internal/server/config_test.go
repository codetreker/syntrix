package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	services "github.com/syntrixbase/syntrix/internal/services/config"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, 8080, cfg.HTTPPort)
	assert.Contains(t, cfg.AllowedHeaders, "X-Syntrix-Expected-Database-Identity")
	assert.Equal(t, 10*time.Second, cfg.HTTPReadTimeout)
	assert.Equal(t, 10*time.Second, cfg.HTTPWriteTimeout)
	assert.Equal(t, 60*time.Second, cfg.HTTPIdleTimeout)
	assert.Equal(t, 9000, cfg.GRPCPort)
	assert.Equal(t, 10*time.Second, cfg.ShutdownTimeout)
}

func TestConfig_ApplyDefaults(t *testing.T) {
	tests := []struct {
		name    string
		initial Config
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name:    "empty config gets all defaults",
			initial: Config{},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "localhost", cfg.Host)
				assert.Equal(t, 8080, cfg.HTTPPort)
				assert.Equal(t, 10*time.Second, cfg.HTTPReadTimeout)
				assert.Equal(t, 10*time.Second, cfg.HTTPWriteTimeout)
				assert.Equal(t, 60*time.Second, cfg.HTTPIdleTimeout)
				assert.Equal(t, 9000, cfg.GRPCPort)
				assert.Equal(t, 10*time.Second, cfg.ShutdownTimeout)
			},
		},
		{
			name: "custom values preserved",
			initial: Config{
				Host:              "0.0.0.0",
				HTTPPort:          8081,
				HTTPReadTimeout:   30 * time.Second,
				HTTPWriteTimeout:  30 * time.Second,
				HTTPIdleTimeout:   120 * time.Second,
				GRPCPort:          9001,
				ShutdownTimeout:   30 * time.Second,
				EnableCORS:        true,
				EnableReflection:  true,
				GRPCMaxConcurrent: 100,
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "0.0.0.0", cfg.Host)
				assert.Equal(t, 8081, cfg.HTTPPort)
				assert.Equal(t, 30*time.Second, cfg.HTTPReadTimeout)
				assert.Equal(t, 30*time.Second, cfg.HTTPWriteTimeout)
				assert.Equal(t, 120*time.Second, cfg.HTTPIdleTimeout)
				assert.Equal(t, 9001, cfg.GRPCPort)
				assert.Equal(t, 30*time.Second, cfg.ShutdownTimeout)
				assert.True(t, cfg.EnableCORS)
				assert.True(t, cfg.EnableReflection)
				assert.Equal(t, uint(100), cfg.GRPCMaxConcurrent)
			},
		},
		{
			name: "partial config gets remaining defaults",
			initial: Config{
				Host:     "prod.example.com",
				HTTPPort: 80,
				// Other fields zero, should get defaults
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "prod.example.com", cfg.Host)
				assert.Equal(t, 80, cfg.HTTPPort)
				assert.Equal(t, 10*time.Second, cfg.HTTPReadTimeout)
				assert.Equal(t, 10*time.Second, cfg.HTTPWriteTimeout)
				assert.Equal(t, 60*time.Second, cfg.HTTPIdleTimeout)
				assert.Equal(t, 9000, cfg.GRPCPort)
				assert.Equal(t, 10*time.Second, cfg.ShutdownTimeout)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.initial
			cfg.ApplyDefaults()
			tt.check(t, &cfg)
		})
	}
}

func TestConfig_ApplyEnvOverrides(t *testing.T) {
	// ApplyEnvOverrides is a no-op for server config
	cfg := DefaultConfig()
	originalHost := cfg.Host

	cfg.ApplyEnvOverrides()

	assert.Equal(t, originalHost, cfg.Host)
}

func TestConfig_ResolvePaths(t *testing.T) {
	// ResolvePaths is a no-op for server config
	cfg := DefaultConfig()

	cfg.ResolvePaths("/some/base/dir", "data")

	// Verify nothing changed
	assert.Equal(t, "localhost", cfg.Host)
}

func TestConfig_Validate(t *testing.T) {
	// Validate is currently a no-op that returns nil
	cfg := Config{}
	err := cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)

	cfg2 := DefaultConfig()
	err = cfg2.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}
