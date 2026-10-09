package config

import (
	"testing"

	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
)

// mockServiceConfig implements ServiceConfig for testing ApplyServiceConfigs
type mockServiceConfig struct {
	defaultsApplied   bool
	envOverridesApply bool
	pathsResolved     bool
	validated         bool
	configDir         string
	dataDir           string
	validateErr       error
	validatedMode     services.DeploymentMode
}

func (m *mockServiceConfig) ApplyDefaults() {
	m.defaultsApplied = true
}

func (m *mockServiceConfig) ApplyEnvOverrides() {
	m.envOverridesApply = true
}

func (m *mockServiceConfig) ResolvePaths(configDir, dataDir string) {
	m.pathsResolved = true
	m.configDir = configDir
	m.dataDir = dataDir
}

func (m *mockServiceConfig) Validate(mode services.DeploymentMode) error {
	m.validated = true
	m.validatedMode = mode
	return m.validateErr
}

func TestApplyServiceConfigs_AllMethodsCalled(t *testing.T) {
	cfg1 := &mockServiceConfig{}
	cfg2 := &mockServiceConfig{}

	err := ApplyServiceConfigs("config", "data", services.ModeDistributed, cfg1, cfg2)

	assert.NoError(t, err)
	assert.True(t, cfg1.defaultsApplied)
	assert.True(t, cfg1.envOverridesApply)
	assert.True(t, cfg1.pathsResolved)
	assert.True(t, cfg1.validated)
	assert.Equal(t, "config", cfg1.configDir)
	assert.Equal(t, "data", cfg1.dataDir)
	assert.Equal(t, services.ModeDistributed, cfg1.validatedMode)

	assert.True(t, cfg2.defaultsApplied)
	assert.True(t, cfg2.envOverridesApply)
	assert.True(t, cfg2.pathsResolved)
	assert.True(t, cfg2.validated)
	assert.Equal(t, "config", cfg2.configDir)
	assert.Equal(t, "data", cfg2.dataDir)
	assert.Equal(t, services.ModeDistributed, cfg2.validatedMode)
}

func TestApplyServiceConfigs_ValidationError(t *testing.T) {
	cfg1 := &mockServiceConfig{}
	cfg2 := &mockServiceConfig{validateErr: assert.AnError}

	err := ApplyServiceConfigs("config", "data", services.ModeStandalone, cfg1, cfg2)

	assert.Error(t, err)
	assert.Equal(t, assert.AnError, err)
}

func TestApplyServiceConfigs_EmptyList(t *testing.T) {
	err := ApplyServiceConfigs("config", "data", services.ModeDistributed)
	assert.NoError(t, err)
}
