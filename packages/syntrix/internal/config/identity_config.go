package config

import (
	identity "github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/internal/gateway/authorization"
	services "github.com/codetreker/syntrix/internal/services/config"
)

// IdentityConfig preserves the identity YAML section while its components own
// authentication and document authorization independently.
type IdentityConfig struct {
	identity.Config `yaml:",inline"`
	AuthZ           authorization.Config `yaml:"authz"`
}

func DefaultIdentityConfig() IdentityConfig {
	return IdentityConfig{
		Config: identity.DefaultConfig(),
		AuthZ:  authorization.DefaultConfig(),
	}
}

func (c *IdentityConfig) ApplyDefaults() {
	c.Config.ApplyDefaults()
	c.AuthZ.ApplyDefaults()
}

func (c *IdentityConfig) ApplyEnvOverrides() {
	c.Config.ApplyEnvOverrides()
	c.AuthZ.ApplyEnvOverrides()
}

func (c *IdentityConfig) ResolvePaths(configDir, dataDir string) {
	c.Config.ResolvePaths(configDir, dataDir)
	c.AuthZ.ResolvePaths(configDir, dataDir)
}

func (c *IdentityConfig) Validate(mode services.DeploymentMode) error {
	return c.AuthZ.Validate(mode)
}
