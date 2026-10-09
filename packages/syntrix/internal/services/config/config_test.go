package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultDeploymentConfig(t *testing.T) {
	cfg := DefaultDeploymentConfig()

	assert.Equal(t, ModeDistributed, cfg.Mode)
}

func TestDeploymentMode_IsStandalone(t *testing.T) {
	tests := []struct {
		mode     DeploymentMode
		expected bool
	}{
		{ModeStandalone, true},
		{ModeDistributed, false},
		{"", false},
		{"other", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.mode.IsStandalone())
		})
	}
}

func TestDeploymentMode_IsDistributed(t *testing.T) {
	tests := []struct {
		mode     DeploymentMode
		expected bool
	}{
		{ModeDistributed, true},
		{"", true}, // empty defaults to distributed
		{ModeStandalone, false},
		{"other", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.mode.IsDistributed())
		})
	}
}

func TestDeploymentConfig_ApplyDefaults(t *testing.T) {
	cfg := &DeploymentConfig{}
	cfg.ApplyDefaults()

	assert.Equal(t, ModeDistributed, cfg.Mode)
}

func TestDeploymentConfig_ApplyEnvOverrides(t *testing.T) {
	os.Setenv("SYNTRIX_DEPLOYMENT_MODE", "standalone")
	defer func() {
		os.Unsetenv("SYNTRIX_DEPLOYMENT_MODE")
	}()

	cfg := DefaultDeploymentConfig()
	cfg.ApplyEnvOverrides()

	assert.Equal(t, ModeStandalone, cfg.Mode)
}

func TestDeploymentConfig_ResolvePaths(t *testing.T) {
	cfg := DefaultDeploymentConfig()
	cfg.ResolvePaths("config", "data")
	// No paths to resolve, just verify no panic
}

func TestDeploymentConfig_Validate(t *testing.T) {
	cfg := DefaultDeploymentConfig()
	err := cfg.Validate(ModeDistributed)
	assert.NoError(t, err)
}

func TestDeploymentConfig_ApplyEnvOverrides_WithTSetenv(t *testing.T) {
	t.Setenv("SYNTRIX_DEPLOYMENT_MODE", "production")

	cfg := DefaultDeploymentConfig()
	cfg.ApplyEnvOverrides()

	assert.Equal(t, DeploymentMode("production"), cfg.Mode)
}

func TestDeploymentConfig_ApplyEnvOverrides_NoEnvVars(t *testing.T) {
	cfg := DefaultDeploymentConfig()
	originalMode := cfg.Mode

	cfg.ApplyEnvOverrides()

	assert.Equal(t, originalMode, cfg.Mode)
}

func TestDeploymentConfig_Validate_EmptyConfig(t *testing.T) {
	cfg := DeploymentConfig{}
	err := cfg.Validate(ModeDistributed)
	assert.NoError(t, err)
}

func TestDeploymentConfig_Validate_InvalidMode(t *testing.T) {
	cfg := DeploymentConfig{Mode: "invalid_mode"}
	err := cfg.Validate(ModeDistributed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "deployment.mode must be 'standalone' or 'distributed'")
}

func TestDeploymentConfig_Validate_ValidModes(t *testing.T) {
	tests := []struct {
		name string
		mode DeploymentMode
	}{
		{"empty mode", ""},
		{"standalone mode", ModeStandalone},
		{"distributed mode", ModeDistributed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DeploymentConfig{Mode: tt.mode}
			err := cfg.Validate(ModeDistributed)
			assert.NoError(t, err)
		})
	}
}
