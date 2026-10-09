package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configYAML := `
name: "test-benchmark"
target: "http://localhost:8080"
duration: 5m
warmup: 30s
workers: 50

auth:
  database: "testdb"

scenario:
  type: "crud"
  operations:
    - type: "create"
      weight: 30
      rate: 100
    - type: "read"
      weight: 50
      rate: 200

data:
  document_size: "1KB"
  fields_count: 10
  seed_data: 100
  cleanup: true

metrics:
  interval: 10s
  histogram:
    buckets: [1, 5, 10, 50, 100]

output:
  console: true
  format: "json"
`

	err := os.WriteFile(configPath, []byte(configYAML), 0644)
	require.NoError(t, err)

	// Load config
	config, err := Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, config)

	// Verify loaded values
	assert.Equal(t, "test-benchmark", config.Name)
	assert.Equal(t, "http://localhost:8080", config.Target)
	assert.Equal(t, 5*time.Minute, config.Duration)
	assert.Equal(t, 30*time.Second, config.Warmup)
	assert.Equal(t, 50, config.Workers)

	assert.Equal(t, "testdb", config.Auth.Database)

	assert.Equal(t, "crud", config.Scenario.Type)
	assert.Len(t, config.Scenario.Operations, 2)

	assert.Equal(t, "create", config.Scenario.Operations[0].Type)
	assert.Equal(t, 30, config.Scenario.Operations[0].Weight)
	assert.Equal(t, 100, config.Scenario.Operations[0].Rate)

	assert.Equal(t, "1KB", config.Data.DocumentSize)
	assert.Equal(t, 10, config.Data.FieldsCount)
	assert.Equal(t, 100, config.Data.SeedData)
	assert.True(t, config.Data.Cleanup)

	assert.Equal(t, 10*time.Second, config.Metrics.Interval)
	assert.Equal(t, []int{1, 5, 10, 50, 100}, config.Metrics.Histogram.Buckets)

	assert.True(t, config.Output.Console)
	assert.Equal(t, "json", config.Output.Format)
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read config file")
}

func TestLoad_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.yaml")

	invalidYAML := `
name: "test"
invalid yaml content [[[
`
	err := os.WriteFile(configPath, []byte(invalidYAML), 0644)
	require.NoError(t, err)

	_, err = Load(configPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse config file")
}

func TestLoad_ValidationError(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Missing required name field
	configYAML := `
target: "http://localhost:8080"
scenario:
  type: "crud"
  operations: []
`
	err := os.WriteFile(configPath, []byte(configYAML), 0644)
	require.NoError(t, err)

	_, err = Load(configPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid configuration")
}

func TestApplyDefaults(t *testing.T) {
	config := &types.Config{
		Name: "test",
		Scenario: types.ScenarioConfig{
			Type: "crud",
			Operations: []types.OperationConfig{
				{Type: "create", Weight: 1, Rate: 10},
			},
		},
	}

	applyDefaults(config)

	assert.Equal(t, "http://localhost:8080", config.Target)
	assert.Equal(t, 5*time.Minute, config.Duration)
	assert.Equal(t, 30*time.Second, config.Warmup)
	assert.Equal(t, 10, config.Workers)
	assert.Equal(t, "default", config.Auth.Database)
	assert.Equal(t, "benchmarks", config.Data.CollectionPrefix)
	assert.Equal(t, 10, config.Data.FieldsCount)
	assert.Equal(t, "1KB", config.Data.DocumentSize)
	assert.True(t, config.Data.Cleanup)
	assert.Equal(t, 10*time.Second, config.Metrics.Interval)
	assert.Len(t, config.Metrics.Histogram.Buckets, 11)
	assert.Equal(t, "localhost:9090", config.Monitor.Address)
	assert.Equal(t, 2*time.Second, config.Monitor.Interval)
	assert.Equal(t, "json", config.Output.Format)
	assert.True(t, config.Output.Console)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		config      *types.Config
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 1, Rate: 10},
					},
				},
				Output: types.OutputConfig{
					Format: "json",
				},
			},
			expectError: false,
		},
		{
			name: "missing name",
			config: &types.Config{
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 1, Rate: 10},
					},
				},
			},
			expectError: true,
			errorMsg:    "benchmark name is required",
		},
		{
			name: "missing target",
			config: &types.Config{
				Name:     "test",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 1, Rate: 10},
					},
				},
			},
			expectError: true,
			errorMsg:    "target URL is required",
		},
		{
			name: "negative duration",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: -1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 1, Rate: 10},
					},
				},
			},
			expectError: true,
			errorMsg:    "duration must be positive",
		},
		{
			name: "zero workers",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  0,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 1, Rate: 10},
					},
				},
			},
			expectError: true,
			errorMsg:    "workers must be positive",
		},
		{
			name: "too many workers",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  20000,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 1, Rate: 10},
					},
				},
			},
			expectError: true,
			errorMsg:    "workers must not exceed 10000",
		},
		{
			name: "missing scenario type",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 1, Rate: 10},
					},
				},
			},
			expectError: true,
			errorMsg:    "scenario type is required",
		},
		{
			name: "invalid scenario type",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type: "invalid",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 1, Rate: 10},
					},
				},
			},
			expectError: true,
			errorMsg:    "invalid scenario type",
		},
		{
			name: "no operations",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type:       "crud",
					Operations: []types.OperationConfig{},
				},
			},
			expectError: true,
			errorMsg:    "at least one operation is required",
		},
		{
			name: "operation missing type",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Weight: 1, Rate: 10},
					},
				},
			},
			expectError: true,
			errorMsg:    "operation[0]: type is required",
		},
		{
			name: "operation negative weight",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: -1, Rate: 10},
					},
				},
			},
			expectError: true,
			errorMsg:    "operation[0]: weight must be non-negative",
		},
		{
			name: "operation negative rate",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 1, Rate: -10},
					},
				},
			},
			expectError: true,
			errorMsg:    "operation[0]: rate must be non-negative",
		},
		{
			name: "zero total weight",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 0, Rate: 10},
					},
				},
			},
			expectError: true,
			errorMsg:    "total operation weight must be positive",
		},
		{
			name: "invalid output format",
			config: &types.Config{
				Name:     "test",
				Target:   "http://localhost:8080",
				Duration: 1 * time.Minute,
				Workers:  10,
				Scenario: types.ScenarioConfig{
					Type: "crud",
					Operations: []types.OperationConfig{
						{Type: "create", Weight: 1, Rate: 10},
					},
				},
				Output: types.OutputConfig{
					Format: "xml",
				},
			},
			expectError: true,
			errorMsg:    "invalid output format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.config)
			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
