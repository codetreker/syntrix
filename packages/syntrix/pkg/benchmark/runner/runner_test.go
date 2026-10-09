package runner

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
	"github.com/codetreker/syntrix/pkg/benchmark/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock implementations for testing

type mockClient struct {
	closed bool
}

func (m *mockClient) CreateDocument(ctx context.Context, collection string, doc map[string]interface{}) (*types.Document, error) {
	return &types.Document{ID: "test-id", Data: doc}, nil
}

func (m *mockClient) GetDocument(ctx context.Context, collection, id string) (*types.Document, error) {
	return &types.Document{ID: id, Data: map[string]interface{}{"test": "data"}}, nil
}

func (m *mockClient) UpdateDocument(ctx context.Context, collection, id string, doc map[string]interface{}) (*types.Document, error) {
	return &types.Document{ID: id, Data: doc}, nil
}

func (m *mockClient) DeleteDocument(ctx context.Context, collection, id string) error {
	return nil
}

func (m *mockClient) Query(ctx context.Context, query types.Query) ([]*types.Document, error) {
	return []*types.Document{{ID: "doc-1", Data: map[string]interface{}{"test": "data"}}}, nil
}

func (m *mockClient) Subscribe(ctx context.Context, query types.Query) (*types.Subscription, error) {
	return &types.Subscription{ID: "sub-1"}, nil
}

func (m *mockClient) Unsubscribe(ctx context.Context, subID string) error {
	return nil
}

func (m *mockClient) Close() error {
	m.closed = true
	return nil
}

type mockScenario struct {
	mu             sync.Mutex
	setupCalled    bool
	teardownCalled bool
	opCount        int
}

func (m *mockScenario) Name() string {
	return "mock-scenario"
}

func (m *mockScenario) Setup(ctx context.Context, env *types.TestEnv) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setupCalled = true
	return nil
}

func (m *mockScenario) NextOperation() (types.Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.opCount >= 5 {
		return nil, nil
	}
	m.opCount++
	return &mockOperation{}, nil
}

func (m *mockScenario) Teardown(ctx context.Context, env *types.TestEnv) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.teardownCalled = true
	return nil
}

type mockOperation struct{}

func (m *mockOperation) Type() string {
	return "mock"
}

func (m *mockOperation) Execute(ctx context.Context, client types.Client) (*types.OperationResult, error) {
	start := time.Now()
	time.Sleep(1 * time.Millisecond)
	return &types.OperationResult{
		OperationType: "mock",
		Success:       true,
		Duration:      time.Since(start),
	}, nil
}

type mockMetricsCollector struct {
	mu          sync.Mutex
	recordCount int
}

func (m *mockMetricsCollector) RecordOperation(result *types.OperationResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordCount++
}

func (m *mockMetricsCollector) GetMetrics() *types.AggregatedMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &types.AggregatedMetrics{
		TotalOperations: int64(m.recordCount),
		TotalErrors:     0,
		SuccessRate:     100.0,
	}
}

func (m *mockMetricsCollector) GetSnapshot() *types.AggregatedMetrics {
	return m.GetMetrics()
}

func (m *mockMetricsCollector) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordCount = 0
}

// Tests

func TestNewBasicRunner(t *testing.T) {
	runner := NewBasicRunner()
	assert.NotNil(t, runner)
	assert.NotNil(t, runner.workers)
	assert.Len(t, runner.workers, 0)
}

func TestBasicRunner_Initialize(t *testing.T) {
	t.Run("valid configuration", func(t *testing.T) {
		runner := NewBasicRunner()
		config := &types.Config{
			Target: "http://localhost:8080",
			Auth: types.AuthConfig{
				Token: utils.GenerateTestToken(),
			},
			Workers:  5,
			Duration: 10 * time.Second,
		}

		err := runner.Initialize(context.Background(), config)
		require.NoError(t, err)
		assert.Equal(t, config, runner.config)
		assert.NotNil(t, runner.client)
		assert.Len(t, runner.workers, 5)
	})

	t.Run("nil configuration", func(t *testing.T) {
		runner := NewBasicRunner()
		err := runner.Initialize(context.Background(), nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config cannot be nil")
	})

	t.Run("empty base URL", func(t *testing.T) {
		runner := NewBasicRunner()
		config := &types.Config{
			Target: "",
			Auth: types.AuthConfig{
				Token: utils.GenerateTestToken(),
			},
			Workers:  1,
			Duration: 1 * time.Second,
		}

		err := runner.Initialize(context.Background(), config)
		assert.Error(t, err)
	})

	t.Run("invalid token", func(t *testing.T) {
		runner := NewBasicRunner()
		config := &types.Config{
			Target: "http://localhost:8080",
			Auth: types.AuthConfig{
				Token: "",
			},
			Workers:  1,
			Duration: 1 * time.Second,
		}

		err := runner.Initialize(context.Background(), config)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "token")
	})
}

func TestBasicRunner_Run_NotInitialized(t *testing.T) {
	runner := NewBasicRunner()
	result, err := runner.Run(context.Background())
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestBasicRunner_Run_WithScenario(t *testing.T) {
	runner := NewBasicRunner()
	config := &types.Config{
		Target: "http://localhost:8080",
		Auth: types.AuthConfig{
			Token: utils.GenerateTestToken(),
		},
		Scenario: types.ScenarioConfig{
			Type: "crud",
		},
		Workers:  2,
		Duration: 100 * time.Millisecond,
	}

	err := runner.Initialize(context.Background(), config)
	require.NoError(t, err)

	// Set mock scenario
	mockScen := &mockScenario{}
	runner.SetScenario(mockScen)

	// Set mock metrics collector
	mockMetrics := &mockMetricsCollector{}
	runner.SetMetricsCollector(mockMetrics)

	// Run benchmark
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result, err := runner.Run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify scenario was called
	assert.True(t, mockScen.setupCalled, "Setup should be called")
	assert.True(t, mockScen.teardownCalled, "Teardown should be called")

	// Verify result
	assert.NotZero(t, result.StartTime)
	assert.NotZero(t, result.EndTime)
	assert.True(t, result.Duration > 0)
}

func TestBasicRunner_Cleanup(t *testing.T) {
	runner := NewBasicRunner()
	config := &types.Config{
		Target: "http://localhost:8080",
		Auth: types.AuthConfig{
			Token: utils.GenerateTestToken(),
		},
		Workers:  1,
		Duration: 1 * time.Second,
	}

	err := runner.Initialize(context.Background(), config)
	require.NoError(t, err)

	// Mock client to verify Close is called
	mockCli := &mockClient{}
	runner.client = mockCli

	err = runner.Cleanup(context.Background())
	assert.NoError(t, err)
	assert.True(t, mockCli.closed, "Client should be closed")
	assert.Nil(t, runner.config)
	assert.Nil(t, runner.client)
}

func TestBasicRunner_SetScenario(t *testing.T) {
	runner := NewBasicRunner()
	mockScen := &mockScenario{}

	runner.SetScenario(mockScen)
	assert.Equal(t, mockScen, runner.scenario)
}

func TestBasicRunner_SetMetricsCollector(t *testing.T) {
	runner := NewBasicRunner()
	mockMetrics := &mockMetricsCollector{}

	runner.SetMetricsCollector(mockMetrics)
	assert.Equal(t, mockMetrics, runner.metrics)
}

func TestBasicRunner_ContextCancellation(t *testing.T) {
	runner := NewBasicRunner()
	config := &types.Config{
		Target: "http://localhost:8080",
		Auth: types.AuthConfig{
			Token: utils.GenerateTestToken(),
		},
		Workers:  1,
		Duration: 10 * time.Second, // Long duration
	}

	err := runner.Initialize(context.Background(), config)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := runner.Run(ctx)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.Duration < 1.0, "Should stop quickly on cancellation")
}
