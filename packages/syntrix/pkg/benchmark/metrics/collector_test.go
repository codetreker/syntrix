package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCollector(t *testing.T) {
	collector := NewCollector()

	assert.NotNil(t, collector)
	assert.NotNil(t, collector.errorsByType)
	assert.NotNil(t, collector.operationMetrics)
	assert.NotNil(t, collector.latencies)
	assert.Equal(t, int64(0), collector.totalOps)
}

func TestCollector_RecordOperation_Success(t *testing.T) {
	collector := NewCollector()

	result := &types.OperationResult{
		OperationType: "create",
		StartTime:     time.Now(),
		Duration:      50 * time.Millisecond,
		Success:       true,
	}

	collector.RecordOperation(result)

	metrics := collector.GetMetrics()
	assert.Equal(t, int64(1), metrics.TotalOperations)
	assert.Equal(t, int64(0), metrics.TotalErrors)
	assert.Equal(t, 100.0, metrics.SuccessRate)
	assert.Equal(t, int64(50), metrics.Latency.Min)
	assert.Equal(t, int64(50), metrics.Latency.Max)
}

func TestCollector_RecordOperation_Failure(t *testing.T) {
	collector := NewCollector()

	result := &types.OperationResult{
		OperationType: "read",
		StartTime:     time.Now(),
		Duration:      30 * time.Millisecond,
		Success:       false,
		Error:         errors.New("not found"),
	}

	collector.RecordOperation(result)

	metrics := collector.GetMetrics()
	assert.Equal(t, int64(1), metrics.TotalOperations)
	assert.Equal(t, int64(1), metrics.TotalErrors)
	assert.Equal(t, 0.0, metrics.SuccessRate)
	assert.Contains(t, metrics.ErrorsByType, "not found")
	assert.Equal(t, int64(1), metrics.ErrorsByType["not found"])
}

func TestCollector_RecordOperation_NilResult(t *testing.T) {
	collector := NewCollector()

	collector.RecordOperation(nil)

	metrics := collector.GetMetrics()
	assert.Equal(t, int64(0), metrics.TotalOperations)
}

func TestCollector_MultipleOperations(t *testing.T) {
	collector := NewCollector()

	// Record 100 successful operations
	for i := 0; i < 100; i++ {
		result := &types.OperationResult{
			OperationType: "create",
			Duration:      time.Duration(i+1) * time.Millisecond,
			Success:       true,
		}
		collector.RecordOperation(result)
	}

	// Record 10 failed operations
	for i := 0; i < 10; i++ {
		result := &types.OperationResult{
			OperationType: "read",
			Duration:      50 * time.Millisecond,
			Success:       false,
			Error:         errors.New("error"),
		}
		collector.RecordOperation(result)
	}

	metrics := collector.GetMetrics()
	assert.Equal(t, int64(110), metrics.TotalOperations)
	assert.Equal(t, int64(10), metrics.TotalErrors)
	assert.InDelta(t, 90.9, metrics.SuccessRate, 0.1)
	assert.Greater(t, metrics.Throughput, 0.0)

	// Check latency stats
	assert.Equal(t, int64(1), metrics.Latency.Min)
	assert.Equal(t, int64(100), metrics.Latency.Max)
	assert.Greater(t, metrics.Latency.Mean, 0.0)
	assert.Greater(t, metrics.Latency.Median, int64(0))
	assert.Greater(t, metrics.Latency.P90, int64(0))
	assert.Greater(t, metrics.Latency.P95, int64(0))
	assert.Greater(t, metrics.Latency.P99, int64(0))
}

func TestCollector_LatencyPercentiles(t *testing.T) {
	collector := NewCollector()

	// Record operations with known latencies
	latencies := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 20, 30, 40, 50, 100}
	for _, lat := range latencies {
		result := &types.OperationResult{
			OperationType: "test",
			Duration:      time.Duration(lat) * time.Millisecond,
			Success:       true,
		}
		collector.RecordOperation(result)
	}

	metrics := collector.GetMetrics()
	assert.Equal(t, int64(1), metrics.Latency.Min)
	assert.Equal(t, int64(100), metrics.Latency.Max)
	// Median of 15 elements is the 8th element (0-indexed: 7)
	assert.Greater(t, metrics.Latency.Median, int64(0))
	assert.LessOrEqual(t, metrics.Latency.Median, int64(10))

	// P90 should be around 50
	assert.GreaterOrEqual(t, metrics.Latency.P90, int64(40))

	// P95 should be around 50-100
	assert.GreaterOrEqual(t, metrics.Latency.P95, int64(50))

	// P99 should be close to max
	assert.GreaterOrEqual(t, metrics.Latency.P99, int64(50))
}

func TestCollector_Throughput(t *testing.T) {
	collector := NewCollector()

	// Record operations
	for i := 0; i < 100; i++ {
		result := &types.OperationResult{
			OperationType: "test",
			Duration:      10 * time.Millisecond,
			Success:       true,
		}
		collector.RecordOperation(result)
	}

	// Wait a bit to ensure elapsed time
	time.Sleep(50 * time.Millisecond)

	metrics := collector.GetMetrics()
	assert.Greater(t, metrics.Throughput, 0.0)
	assert.Less(t, metrics.Throughput, 10000.0) // Sanity check
}

func TestCollector_ErrorTracking(t *testing.T) {
	collector := NewCollector()

	errorCounts := map[string]int{
		"not found":    10,
		"timeout":      5,
		"unauthorized": 3,
	}

	for errMsg, count := range errorCounts {
		for i := 0; i < count; i++ {
			result := &types.OperationResult{
				OperationType: "test",
				Duration:      10 * time.Millisecond,
				Success:       false,
				Error:         errors.New(errMsg),
			}
			collector.RecordOperation(result)
		}
	}

	metrics := collector.GetMetrics()
	assert.Equal(t, int64(18), metrics.TotalErrors)
	assert.Len(t, metrics.ErrorsByType, 3)

	for errMsg, count := range errorCounts {
		assert.Equal(t, int64(count), metrics.ErrorsByType[errMsg])
	}
}

func TestCollector_GetOperationMetrics(t *testing.T) {
	collector := NewCollector()

	// Record different operation types
	operations := map[string]int{
		"create": 50,
		"read":   30,
		"update": 15,
		"delete": 5,
	}

	for opType, count := range operations {
		for i := 0; i < count; i++ {
			result := &types.OperationResult{
				OperationType: opType,
				Duration:      time.Duration(i+1) * time.Millisecond,
				Success:       true,
			}
			collector.RecordOperation(result)
		}
	}

	opMetrics := collector.GetOperationMetrics()
	assert.Len(t, opMetrics, 4)

	for opType, expectedCount := range operations {
		metrics, exists := opMetrics[opType]
		require.True(t, exists, "Metrics for operation %s should exist", opType)
		assert.Equal(t, int64(expectedCount), metrics.TotalOperations)
		assert.Equal(t, 100.0, metrics.SuccessRate)
		assert.Greater(t, metrics.Throughput, 0.0)
	}
}

func TestCollector_Reset(t *testing.T) {
	collector := NewCollector()

	// Record some operations
	for i := 0; i < 10; i++ {
		result := &types.OperationResult{
			OperationType: "test",
			Duration:      10 * time.Millisecond,
			Success:       true,
		}
		collector.RecordOperation(result)
	}

	metrics := collector.GetMetrics()
	assert.Equal(t, int64(10), metrics.TotalOperations)

	// Reset
	collector.Reset()

	metricsAfter := collector.GetMetrics()
	assert.Equal(t, int64(0), metricsAfter.TotalOperations)
	assert.Equal(t, int64(0), metricsAfter.TotalErrors)
	assert.Equal(t, 0.0, metricsAfter.SuccessRate)
	assert.Len(t, metricsAfter.ErrorsByType, 0)
}

func TestCollector_GetSnapshot(t *testing.T) {
	collector := NewCollector()

	result := &types.OperationResult{
		OperationType: "test",
		Duration:      10 * time.Millisecond,
		Success:       true,
	}
	collector.RecordOperation(result)

	snapshot1 := collector.GetSnapshot()
	snapshot2 := collector.GetSnapshot()

	// Snapshots should be independent
	assert.NotSame(t, snapshot1, snapshot2)
	assert.Equal(t, snapshot1.TotalOperations, snapshot2.TotalOperations)
}

func TestCollector_Concurrency(t *testing.T) {
	collector := NewCollector()

	done := make(chan bool, 100)

	// Record operations concurrently
	for i := 0; i < 100; i++ {
		go func(idx int) {
			result := &types.OperationResult{
				OperationType: "test",
				Duration:      time.Duration(idx) * time.Millisecond,
				Success:       idx%10 != 0, // 10% failure rate
				Error:         nil,
			}
			if !result.Success {
				result.Error = errors.New("test error")
			}
			collector.RecordOperation(result)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 100; i++ {
		<-done
	}

	metrics := collector.GetMetrics()
	assert.Equal(t, int64(100), metrics.TotalOperations)
	assert.Equal(t, int64(10), metrics.TotalErrors)
	assert.Equal(t, 90.0, metrics.SuccessRate)
}

func TestCollector_SuccessRateEdgeCases(t *testing.T) {
	t.Run("no operations", func(t *testing.T) {
		collector := NewCollector()
		metrics := collector.GetMetrics()
		assert.Equal(t, 0.0, metrics.SuccessRate)
	})

	t.Run("all success", func(t *testing.T) {
		collector := NewCollector()
		for i := 0; i < 10; i++ {
			collector.RecordOperation(&types.OperationResult{
				OperationType: "test",
				Duration:      10 * time.Millisecond,
				Success:       true,
			})
		}
		metrics := collector.GetMetrics()
		assert.Equal(t, 100.0, metrics.SuccessRate)
	})

	t.Run("all failures", func(t *testing.T) {
		collector := NewCollector()
		for i := 0; i < 10; i++ {
			collector.RecordOperation(&types.OperationResult{
				OperationType: "test",
				Duration:      10 * time.Millisecond,
				Success:       false,
				Error:         errors.New("error"),
			})
		}
		metrics := collector.GetMetrics()
		assert.Equal(t, 0.0, metrics.SuccessRate)
	})
}

func TestCollector_MinMaxLatency(t *testing.T) {
	collector := NewCollector()

	// Record first operation
	collector.RecordOperation(&types.OperationResult{
		OperationType: "test",
		Duration:      50 * time.Millisecond,
		Success:       true,
	})

	metrics := collector.GetMetrics()
	assert.Equal(t, int64(50), metrics.Latency.Min)
	assert.Equal(t, int64(50), metrics.Latency.Max)

	// Record lower latency
	collector.RecordOperation(&types.OperationResult{
		OperationType: "test",
		Duration:      10 * time.Millisecond,
		Success:       true,
	})

	metrics = collector.GetMetrics()
	assert.Equal(t, int64(10), metrics.Latency.Min)
	assert.Equal(t, int64(50), metrics.Latency.Max)

	// Record higher latency
	collector.RecordOperation(&types.OperationResult{
		OperationType: "test",
		Duration:      100 * time.Millisecond,
		Success:       true,
	})

	metrics = collector.GetMetrics()
	assert.Equal(t, int64(10), metrics.Latency.Min)
	assert.Equal(t, int64(100), metrics.Latency.Max)
}
