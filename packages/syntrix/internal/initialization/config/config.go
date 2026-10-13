package config

import (
	"fmt"

	services "github.com/codetreker/syntrix/internal/services/config"
)

type Mode string

const (
	Operator Mode = "operator"
	Managed  Mode = "managed"
)

type Config struct {
	Mode Mode `yaml:"mode"`
}

func DefaultConfig() Config { return Config{Mode: Operator} }
func (c *Config) ApplyDefaults() {
	if c.Mode == "" {
		c.Mode = Operator
	}
}
func (c Config) Validate(_ services.DeploymentMode) error {
	if c.Mode != Managed && c.Mode != Operator {
		return fmt.Errorf("initialization.mode must be managed or operator")
	}
	return nil
}
func (c *Config) ApplyEnvOverrides()       { _ = c }
func (c *Config) ResolvePaths(_, _ string) { _ = c }
