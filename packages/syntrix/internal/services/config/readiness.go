package config

import (
	"fmt"
	"slices"
	"time"

	"github.com/codetreker/syntrix/internal/core/origin"
)

type ReadinessConfig struct {
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

type Config struct {
	Readiness ReadinessConfig `yaml:"readiness"`
}

func DefaultConfig() Config {
	return Config{Readiness: ReadinessConfig{Interval: time.Second, Timeout: 2 * time.Second}}
}
func (c *Config) ApplyDefaults() {
	if c.Readiness.Interval == 0 {
		c.Readiness.Interval = time.Second
	}
	if c.Readiness.Timeout == 0 {
		c.Readiness.Timeout = 2 * time.Second
	}
}
func (c *Config) ApplyEnvOverrides()          { _ = c }
func (c *Config) ResolvePaths(string, string) { _ = c }
func (c *Config) Validate(DeploymentMode) error {
	if c.Readiness.Interval <= 0 || c.Readiness.Interval > time.Minute {
		return fmt.Errorf("services.readiness.interval must be positive and at most 1m")
	}
	if c.Readiness.Timeout <= 0 || c.Readiness.Timeout > time.Minute {
		return fmt.Errorf("services.readiness.timeout must be positive and at most 1m")
	}
	return nil
}

func ValidateOrigins(managed bool, cors bool, httpPolicy, realtimePolicy origin.Policy) error {
	if err := httpPolicy.Validate(managed); err != nil {
		return err
	}
	if err := realtimePolicy.Validate(managed); err != nil {
		return err
	}
	if managed && !cors {
		return fmt.Errorf("managed runtime requires REST origin enforcement")
	}
	httpOrigins := slices.Clone(httpPolicy.AllowedOrigins)
	realtimeOrigins := slices.Clone(realtimePolicy.AllowedOrigins)
	slices.Sort(httpOrigins)
	slices.Sort(realtimeOrigins)
	if !slices.Equal(httpOrigins, realtimeOrigins) || httpPolicy.AllowDevelopment != realtimePolicy.AllowDevelopment {
		return fmt.Errorf("runtime REST and realtime origins must use one policy")
	}
	return nil
}
