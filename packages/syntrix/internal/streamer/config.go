package streamer

import (
	"fmt"

	services "github.com/codetreker/syntrix/internal/services/config"
)

type Config struct {
	// Server configuration for the Streamer service.
	Server ServerConfig `yaml:"server"`
	Client ClientConfig `yaml:"client"`
}

func DefaultConfig() Config {
	return Config{
		Server: DefaultServiceConfig(),
		Client: DefaultClientConfig(),
	}
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	c.Server.ApplyDefaults()
	c.Client.ApplyDefaults()
}

// ApplyEnvOverrides applies environment variable overrides.
// No env vars for streamer config currently.
func (c *Config) ApplyEnvOverrides() { _ = c }

// ResolvePaths resolves relative paths using the given directories.
// No paths to resolve in streamer config.
func (c *Config) ResolvePaths(_, _ string) { _ = c }

// Validate returns an error if the configuration is invalid.
func (c *Config) Validate(mode services.DeploymentMode) error {
	if mode.IsDistributed() && c.Server.PullerAddr == "" {
		return fmt.Errorf("streamer.server.puller_addr is required in distributed mode")
	}
	return nil
}
