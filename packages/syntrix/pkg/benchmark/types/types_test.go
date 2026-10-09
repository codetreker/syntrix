package types

import (
	"testing"
	"time"
)

// TestConfigStructure tests that Config struct can be instantiated.
func TestConfigStructure(t *testing.T) {
	config := &Config{
		Name:     "test-benchmark",
		Target:   "http://localhost:8080",
		Duration: 5 * time.Minute,
		Warmup:   30 * time.Second,
		Workers:  10,
	}

	if config.Name != "test-benchmark" {
		t.Errorf("Expected Name to be 'test-benchmark', got %s", config.Name)
	}

	if config.Workers != 10 {
		t.Errorf("Expected Workers to be 10, got %d", config.Workers)
	}
}

// TestOperationResult tests OperationResult structure.
func TestOperationResult(t *testing.T) {
	result := &OperationResult{
		OperationType: "create",
		StartTime:     time.Now(),
		Duration:      100 * time.Millisecond,
		Success:       true,
		StatusCode:    201,
	}

	if result.OperationType != "create" {
		t.Errorf("Expected OperationType to be 'create', got %s", result.OperationType)
	}

	if !result.Success {
		t.Error("Expected Success to be true")
	}

	if result.StatusCode != 201 {
		t.Errorf("Expected StatusCode to be 201, got %d", result.StatusCode)
	}
}

// TestAggregatedMetrics tests AggregatedMetrics structure.
func TestAggregatedMetrics(t *testing.T) {
	metrics := &AggregatedMetrics{
		TotalOperations: 1000,
		TotalErrors:     10,
		SuccessRate:     99.0,
		Throughput:      200.5,
		Latency: LatencyStats{
			Min:    5,
			Max:    500,
			Mean:   45.5,
			Median: 40,
			P90:    80,
			P95:    120,
			P99:    250,
		},
	}

	if metrics.TotalOperations != 1000 {
		t.Errorf("Expected TotalOperations to be 1000, got %d", metrics.TotalOperations)
	}

	if metrics.SuccessRate != 99.0 {
		t.Errorf("Expected SuccessRate to be 99.0, got %f", metrics.SuccessRate)
	}

	if metrics.Latency.P99 != 250 {
		t.Errorf("Expected Latency.P99 to be 250, got %d", metrics.Latency.P99)
	}
}

// TestBenchmarkStatus tests BenchmarkStatus constants.
func TestBenchmarkStatus(t *testing.T) {
	statuses := []BenchmarkStatus{
		StatusStarting,
		StatusWarmup,
		StatusRamping,
		StatusRunning,
		StatusCompleted,
		StatusFailed,
		StatusCancelled,
	}

	expectedStatuses := []string{
		"starting",
		"warmup",
		"ramping",
		"running",
		"completed",
		"failed",
		"cancelled",
	}

	for i, status := range statuses {
		if string(status) != expectedStatuses[i] {
			t.Errorf("Expected status %s, got %s", expectedStatuses[i], string(status))
		}
	}
}

// TestDocument tests Document structure.
func TestDocument(t *testing.T) {
	doc := &Document{
		ID:         "doc-123",
		Collection: "users",
		Data: map[string]interface{}{
			"name": "Test User",
			"age":  30,
		},
		Version: 1,
	}

	if doc.ID != "doc-123" {
		t.Errorf("Expected ID to be 'doc-123', got %s", doc.ID)
	}

	if doc.Data["name"] != "Test User" {
		t.Errorf("Expected name to be 'Test User', got %v", doc.Data["name"])
	}
}

// TestSubscriptionClose tests Subscription Close method.
func TestSubscriptionClose(t *testing.T) {
	closed := false
	sub := &Subscription{
		ID: "sub-123",
		closeFn: func() error {
			closed = true
			return nil
		},
	}

	err := sub.Close()
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if !closed {
		t.Error("Expected closeFn to be called")
	}

	// Test subscription with nil closeFn
	sub2 := &Subscription{
		ID:      "sub-456",
		closeFn: nil,
	}

	err = sub2.Close()
	if err != nil {
		t.Errorf("Expected no error with nil closeFn, got %v", err)
	}
}

// TestTestEnv tests TestEnv structure.
func TestTestEnv(t *testing.T) {
	env := &TestEnv{
		BaseURL: "http://localhost:8080",
		Token:   "test-token-123",
		Config: &Config{
			Name: "test-env",
		},
	}

	if env.BaseURL != "http://localhost:8080" {
		t.Errorf("Expected BaseURL to be 'http://localhost:8080', got %s", env.BaseURL)
	}

	if env.Token != "test-token-123" {
		t.Errorf("Expected Token to be 'test-token-123', got %s", env.Token)
	}
}
