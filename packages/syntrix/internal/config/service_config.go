package config

import (
	services "github.com/codetreker/syntrix/internal/services/config"
)

// ServiceConfig defines the standard configuration lifecycle methods.
// Each service config should implement this interface to ensure consistent
// configuration handling across the application.
type ServiceConfig interface {
	// ApplyDefaults fills zero values with sensible defaults
	ApplyDefaults()

	// ApplyEnvOverrides applies environment variable overrides
	ApplyEnvOverrides()

	// ResolvePaths resolves relative paths using the given directories.
	// - configDir: base directory for config-related paths (e.g., rules_path, template_path)
	// - dataDir: base directory for runtime data paths (e.g., logs, caches, indexes)
	ResolvePaths(configDir, dataDir string)

	// Validate returns an error if the configuration is invalid.
	// The mode parameter allows configs to perform mode-specific validation,
	// such as requiring service addresses in distributed mode.
	Validate(mode services.DeploymentMode) error
}

// ApplyServiceConfigs applies the configuration lifecycle to all service configs.
// It calls ApplyDefaults, ApplyEnvOverrides, ResolvePaths, and Validate in order.
func ApplyServiceConfigs(configDir, dataDir string, mode services.DeploymentMode, configs ...ServiceConfig) error {
	for _, cfg := range configs {
		cfg.ApplyDefaults()
		cfg.ApplyEnvOverrides()
		cfg.ResolvePaths(configDir, dataDir)
		if err := cfg.Validate(mode); err != nil {
			return err
		}
	}
	return nil
}
