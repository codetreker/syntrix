// Package metrics provides metrics collection and aggregation for benchmarks.
package metrics

import (
	"sort"
	"sync"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
)

// Collector implements the MetricsCollector interface.
type Collector struct {
	startTime time.Time
	mu        sync.RWMutex

	// Operation counts
	totalOps      int64
	successfulOps int64
	failedOps     int64

	// Latency tracking
	latencies    []int64 // in milliseconds
	minLatency   int64
	maxLatency   int64
	totalLatency int64

	// Error tracking
	errorsByType map[string]int64

	// Per-operation metrics
	operationMetrics map[string]*operationStats
}

type operationStats struct {
	count        int64
	successCount int64
	failCount    int64
	totalLatency int64
	latencies    []int64
}

// NewCollector creates a new metrics collector.
func NewCollector() *Collector {
	return &Collector{
		startTime:        time.Now(),
		errorsByType:     make(map[string]int64),
		operationMetrics: make(map[string]*operationStats),
		latencies:        make([]int64, 0, 10000),
		minLatency:       -1,
		maxLatency:       0,
	}
}

// RecordOperation records the result of a single operation.
func (c *Collector) RecordOperation(result *types.OperationResult) {
	if result == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update global counts
	c.totalOps++
	if result.Success {
		c.successfulOps++
	} else {
		c.failedOps++
		if result.Error != nil {
			c.errorsByType[result.Error.Error()]++
		}
	}

	// Update latency stats
	latencyMs := result.Duration.Milliseconds()
	c.latencies = append(c.latencies, latencyMs)
	c.totalLatency += latencyMs

	if c.minLatency == -1 || latencyMs < c.minLatency {
		c.minLatency = latencyMs
	}
	if latencyMs > c.maxLatency {
		c.maxLatency = latencyMs
	}

	// Update per-operation stats
	opType := result.OperationType
	if c.operationMetrics[opType] == nil {
		c.operationMetrics[opType] = &operationStats{
			latencies: make([]int64, 0, 1000),
		}
	}

	opStats := c.operationMetrics[opType]
	opStats.count++
	if result.Success {
		opStats.successCount++
	} else {
		opStats.failCount++
	}
	opStats.totalLatency += latencyMs
	opStats.latencies = append(opStats.latencies, latencyMs)
}

// GetMetrics returns current aggregated metrics.
func (c *Collector) GetMetrics() *types.AggregatedMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.buildMetrics()
}

// GetSnapshot returns a snapshot of current metrics (thread-safe copy).
func (c *Collector) GetSnapshot() *types.AggregatedMetrics {
	return c.GetMetrics()
}

// Reset resets all metrics.
func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.startTime = time.Now()
	c.totalOps = 0
	c.successfulOps = 0
	c.failedOps = 0
	c.latencies = c.latencies[:0]
	c.minLatency = -1
	c.maxLatency = 0
	c.totalLatency = 0
	c.errorsByType = make(map[string]int64)
	c.operationMetrics = make(map[string]*operationStats)
}

// buildMetrics builds the aggregated metrics (caller must hold lock).
func (c *Collector) buildMetrics() *types.AggregatedMetrics {
	metrics := &types.AggregatedMetrics{
		TotalOperations: c.totalOps,
		TotalErrors:     c.failedOps,
		ErrorsByType:    make(map[string]int64),
	}

	// Copy error stats
	for errType, count := range c.errorsByType {
		metrics.ErrorsByType[errType] = count
	}

	// Calculate success rate
	if c.totalOps > 0 {
		metrics.SuccessRate = float64(c.successfulOps) / float64(c.totalOps) * 100
	}

	// Calculate throughput
	elapsed := time.Since(c.startTime).Seconds()
	if elapsed > 0 {
		metrics.Throughput = float64(c.totalOps) / elapsed
	}

	// Calculate latency stats
	if len(c.latencies) > 0 {
		latenciesCopy := make([]int64, len(c.latencies))
		copy(latenciesCopy, c.latencies)
		sort.Slice(latenciesCopy, func(i, j int) bool {
			return latenciesCopy[i] < latenciesCopy[j]
		})

		metrics.Latency = types.LatencyStats{
			Min:    c.minLatency,
			Max:    c.maxLatency,
			Mean:   float64(c.totalLatency) / float64(len(c.latencies)),
			Median: latenciesCopy[len(latenciesCopy)/2],
			P90:    latenciesCopy[int(float64(len(latenciesCopy))*0.90)],
			P95:    latenciesCopy[int(float64(len(latenciesCopy))*0.95)],
			P99:    latenciesCopy[int(float64(len(latenciesCopy))*0.99)],
		}
	}

	return metrics
}

// GetOperationMetrics returns per-operation metrics.
func (c *Collector) GetOperationMetrics() map[string]*types.AggregatedMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]*types.AggregatedMetrics)

	for opType, stats := range c.operationMetrics {
		opMetrics := &types.AggregatedMetrics{
			TotalOperations: stats.count,
			TotalErrors:     stats.failCount,
		}

		if stats.count > 0 {
			opMetrics.SuccessRate = float64(stats.successCount) / float64(stats.count) * 100
		}

		// Calculate throughput
		elapsed := time.Since(c.startTime).Seconds()
		if elapsed > 0 {
			opMetrics.Throughput = float64(stats.count) / elapsed
		}

		// Calculate latency stats
		if len(stats.latencies) > 0 {
			latenciesCopy := make([]int64, len(stats.latencies))
			copy(latenciesCopy, stats.latencies)
			sort.Slice(latenciesCopy, func(i, j int) bool {
				return latenciesCopy[i] < latenciesCopy[j]
			})

			minLat := latenciesCopy[0]
			maxLat := latenciesCopy[len(latenciesCopy)-1]

			opMetrics.Latency = types.LatencyStats{
				Min:    minLat,
				Max:    maxLat,
				Mean:   float64(stats.totalLatency) / float64(len(stats.latencies)),
				Median: latenciesCopy[len(latenciesCopy)/2],
				P90:    latenciesCopy[int(float64(len(latenciesCopy))*0.90)],
				P95:    latenciesCopy[int(float64(len(latenciesCopy))*0.95)],
				P99:    latenciesCopy[int(float64(len(latenciesCopy))*0.99)],
			}
		}

		result[opType] = opMetrics
	}

	return result
}
