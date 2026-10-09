package config

import (
	"fmt"
	"os"
)

// DeploymentMode represents the deployment mode of the service.
type DeploymentMode string

const (
	// ModeDistributed is the default mode where services communicate via gRPC/NATS.
	ModeDistributed DeploymentMode = "distributed"
	// ModeStandalone runs all services in a single process with direct function calls.
	ModeStandalone DeploymentMode = "standalone"
)

// IsStandalone returns true if this is standalone mode.
func (m DeploymentMode) IsStandalone() bool {
	return m == ModeStandalone
}

// IsDistributed returns true if this is distributed mode.
// Empty string defaults to distributed.
func (m DeploymentMode) IsDistributed() bool {
	return m == "" || m == ModeDistributed
}

// DeploymentConfig holds deployment mode settings
type DeploymentConfig struct {
	Mode DeploymentMode `yaml:"mode"` // "standalone" or "distributed" (default)
}

// StandaloneConfig holds standalone-specific settings
type StandaloneConfig struct {
	EmbeddedNATS bool `yaml:"embedded_nats"` // Use embedded NATS server
}

func DefaultDeploymentConfig() DeploymentConfig {
	return DeploymentConfig{
		Mode: ModeDistributed, // Default to distributed mode
	}
}

// ApplyDefaults fills in zero values with defaults.
func (c *DeploymentConfig) ApplyDefaults() {
	defaults := DefaultDeploymentConfig()
	if c.Mode == "" {
		c.Mode = defaults.Mode
	}
}

// ApplyEnvOverrides applies environment variable overrides.
func (c *DeploymentConfig) ApplyEnvOverrides() {
	if val := os.Getenv("SYNTRIX_DEPLOYMENT_MODE"); val != "" {
		c.Mode = DeploymentMode(val)
	}
}

// ResolvePaths resolves relative paths using the given directories.
// No paths to resolve in deployment config.
func (c *DeploymentConfig) ResolvePaths(_, _ string) { _ = c }

// Validate returns an error if the configuration is invalid.
func (c *DeploymentConfig) Validate(_ DeploymentMode) error {
	// Validate Deployment Mode
	if c.Mode != "" && c.Mode != ModeStandalone && c.Mode != ModeDistributed {
		return fmt.Errorf("deployment.mode must be 'standalone' or 'distributed', got '%s'", c.Mode)
	}

	return nil
}
