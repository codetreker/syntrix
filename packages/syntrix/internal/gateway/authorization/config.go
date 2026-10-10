package authorization

import (
	"errors"
	"path/filepath"

	services "github.com/codetreker/syntrix/internal/services/config"
)

type Config struct {
	RulesPath string `yaml:"rules_path"`
}

func DefaultConfig() Config {
	return Config{RulesPath: "security_rules"}
}

func (c *Config) ApplyDefaults() {
	if c.RulesPath == "" {
		c.RulesPath = DefaultConfig().RulesPath
	}
}

func (c *Config) ApplyEnvOverrides() { _ = c }

func (c *Config) ResolvePaths(configDir, _ string) {
	if c.RulesPath != "" && !filepath.IsAbs(c.RulesPath) {
		c.RulesPath = filepath.Join(configDir, c.RulesPath)
	}
}

func (c *Config) Validate(_ services.DeploymentMode) error {
	if c.RulesPath == "" {
		return errors.New("identity.authz.rules_path is required")
	}
	return nil
}
