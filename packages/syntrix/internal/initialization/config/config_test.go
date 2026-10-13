package config

import (
	"testing"

	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
)

func TestModeConfig(t *testing.T) {
	c := Config{}
	c.ApplyDefaults()
	assert.Equal(t, DefaultConfig(), c)
	assert.NoError(t, c.Validate(services.ModeStandalone))
	c.Mode = Managed
	c.ApplyDefaults()
	assert.Equal(t, Managed, c.Mode)
	assert.NoError(t, c.Validate(services.ModeDistributed))
	c.Mode = "unknown"
	assert.Error(t, c.Validate(services.ModeStandalone))
	c.ApplyEnvOverrides()
	c.ResolvePaths("a", "b")
	assert.Equal(t, Mode("unknown"), c.Mode)
}
