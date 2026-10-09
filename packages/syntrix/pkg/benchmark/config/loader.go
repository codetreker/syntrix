// Package config provides configuration loading and validation for the benchmark tool.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
	"gopkg.in/yaml.v3"
)

// Load loads configuration from a YAML file.
func Load(path string) (*types.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config types.Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Set base directory from config file path
	// If config file is "configs/benchmark.yaml", baseDir becomes "configs"
	configDir := filepath.Dir(path)
	config.BaseDir = configDir

	// Set defaults
	applyDefaults(&config)

	// Resolve relative paths based on config directory
	resolvePaths(&config)

	// Validate
	if err := Validate(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

// applyDefaults applies default values to the configuration.
func applyDefaults(config *types.Config) {
	if config.Target == "" {
		config.Target = "http://localhost:8080"
	}

	if config.Duration == 0 {
		config.Duration = 5 * time.Minute
	}

	if config.Warmup == 0 {
		config.Warmup = 30 * time.Second
	}

	if config.Workers == 0 {
		config.Workers = 10
	}

	if config.Auth.Database == "" {
		config.Auth.Database = "default"
	}

	if config.Auth.PrivateKeyFile == "" {
		config.Auth.PrivateKeyFile = "keys/auth_private.pem"
	}

	if config.Data.CollectionPrefix == "" {
		config.Data.CollectionPrefix = "benchmarks"
	}

	if config.Data.FieldsCount == 0 {
		config.Data.FieldsCount = 10
	}

	if config.Data.DocumentSize == "" {
		config.Data.DocumentSize = "1KB"
	}

	if !config.Data.Cleanup {
		config.Data.Cleanup = true
	}

	if config.Metrics.Interval == 0 {
		config.Metrics.Interval = 10 * time.Second
	}

	if len(config.Metrics.Histogram.Buckets) == 0 {
		config.Metrics.Histogram.Buckets = []int{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}
	}

	if config.Monitor.Address == "" {
		config.Monitor.Address = "localhost:9090"
	}

	if config.Monitor.Interval == 0 {
		config.Monitor.Interval = 2 * time.Second
	}

	if config.Output.Format == "" {
		config.Output.Format = "json"
	}

	if !config.Output.Console {
		config.Output.Console = true
	}
}

// resolvePaths resolves relative paths based on the config base directory.
func resolvePaths(config *types.Config) {
	if config.BaseDir == "" {
		return
	}

	// Resolve private key file path
	if config.Auth.PrivateKeyFile != "" && !filepath.IsAbs(config.Auth.PrivateKeyFile) {
		config.Auth.PrivateKeyFile = filepath.Join(config.BaseDir, config.Auth.PrivateKeyFile)
	}

	// Resolve output file path if specified
	if config.Output.File != "" && !filepath.IsAbs(config.Output.File) {
		config.Output.File = filepath.Join(config.BaseDir, config.Output.File)
	}
}

// Validate validates the configuration.
func Validate(config *types.Config) error {
	if config.Name == "" {
		return fmt.Errorf("benchmark name is required")
	}

	if config.Target == "" {
		return fmt.Errorf("target URL is required")
	}

	if config.Duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}

	if config.Workers <= 0 {
		return fmt.Errorf("workers must be positive")
	}

	if config.Workers > 10000 {
		return fmt.Errorf("workers must not exceed 10000")
	}

	if config.Scenario.Type == "" {
		return fmt.Errorf("scenario type is required")
	}

	validScenarios := map[string]bool{
		"crud":     true,
		"query":    true,
		"realtime": true,
		"mixed":    true,
	}

	if !validScenarios[config.Scenario.Type] {
		return fmt.Errorf("invalid scenario type: %s (must be crud, query, realtime, or mixed)", config.Scenario.Type)
	}

	if len(config.Scenario.Operations) == 0 {
		return fmt.Errorf("at least one operation is required in scenario")
	}

	// Validate operations
	totalWeight := 0
	for i, op := range config.Scenario.Operations {
		if op.Type == "" {
			return fmt.Errorf("operation[%d]: type is required", i)
		}

		if op.Weight < 0 {
			return fmt.Errorf("operation[%d]: weight must be non-negative", i)
		}

		if op.Rate < 0 {
			return fmt.Errorf("operation[%d]: rate must be non-negative", i)
		}

		totalWeight += op.Weight
	}

	if totalWeight == 0 {
		return fmt.Errorf("total operation weight must be positive")
	}

	// Validate output format
	validFormats := map[string]bool{
		"json":       true,
		"prometheus": true,
		"csv":        true,
	}

	if !validFormats[config.Output.Format] {
		return fmt.Errorf("invalid output format: %s (must be json, prometheus, or csv)", config.Output.Format)
	}

	return nil
}
