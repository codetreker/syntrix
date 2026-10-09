package reporter

import (
	"bytes"
	"testing"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewConsoleReporter(t *testing.T) {
	buf := &bytes.Buffer{}
	reporter := NewConsoleReporter(buf, false)

	assert.NotNil(t, reporter)
	assert.Equal(t, buf, reporter.writer)
	assert.False(t, reporter.colors)
}

func TestConsoleReporter_ReportProgress(t *testing.T) {
	t.Run("with metrics", func(t *testing.T) {
		buf := &bytes.Buffer{}
		reporter := NewConsoleReporter(buf, false)

		metrics := &types.AggregatedMetrics{
			TotalOperations: 1000,
			SuccessRate:     98.5,
			Throughput:      123.45,
			Latency: types.LatencyStats{
				P99: 50,
			},
		}

		reporter.ReportProgress(5*time.Second, metrics)

		output := buf.String()
		assert.Contains(t, output, "1000")
		assert.Contains(t, output, "98.5%")
		assert.Contains(t, output, "123.45")
		assert.Contains(t, output, "50ms")
	})

	t.Run("with nil metrics", func(t *testing.T) {
		buf := &bytes.Buffer{}
		reporter := NewConsoleReporter(buf, false)

		reporter.ReportProgress(5*time.Second, nil)

		assert.Empty(t, buf.String())
	})
}

func TestConsoleReporter_ReportSummary(t *testing.T) {
	t.Run("complete result", func(t *testing.T) {
		buf := &bytes.Buffer{}
		reporter := NewConsoleReporter(buf, false)

		result := &types.Result{
			StartTime: time.Now().Add(-10 * time.Second),
			EndTime:   time.Now(),
			Duration:  10.0,
			Summary: &types.AggregatedMetrics{
				TotalOperations: 5000,
				TotalErrors:     50,
				SuccessRate:     99.0,
				Throughput:      500.0,
				Latency: types.LatencyStats{
					Min:    5,
					Max:    100,
					Mean:   25.5,
					Median: 20,
					P90:    45,
					P95:    60,
					P99:    85,
				},
				ErrorsByType: map[string]int64{
					"timeout":   30,
					"not found": 20,
				},
			},
			Operations: map[string]*types.AggregatedMetrics{
				"create": {
					TotalOperations: 1500,
					SuccessRate:     100.0,
					Throughput:      150.0,
					Latency: types.LatencyStats{
						Min:    10,
						Median: 15,
						P99:    30,
						Max:    40,
					},
				},
				"read": {
					TotalOperations: 2000,
					SuccessRate:     98.0,
					Throughput:      200.0,
					Latency: types.LatencyStats{
						Min:    5,
						Median: 10,
						P99:    25,
						Max:    35,
					},
				},
			},
		}

		err := reporter.ReportSummary(result)
		require.NoError(t, err)

		output := buf.String()

		// Check header
		assert.Contains(t, output, "Benchmark Results")

		// Check session info
		assert.Contains(t, output, "Session Information")
		assert.Contains(t, output, "Duration:")
		assert.Contains(t, output, "Started:")
		assert.Contains(t, output, "Finished:")

		// Check overall metrics
		assert.Contains(t, output, "Overall Performance")
		assert.Contains(t, output, "5000")
		assert.Contains(t, output, "99.00%")
		assert.Contains(t, output, "500.00")

		// Check latency stats
		assert.Contains(t, output, "Min:")
		assert.Contains(t, output, "Median:")
		assert.Contains(t, output, "P99:")

		// Check per-operation metrics
		assert.Contains(t, output, "Per-Operation Metrics")
		assert.Contains(t, output, "CREATE")
		assert.Contains(t, output, "READ")

		// Check errors
		assert.Contains(t, output, "Errors")
		assert.Contains(t, output, "50")
		assert.Contains(t, output, "timeout")
		assert.Contains(t, output, "not found")
	})

	t.Run("nil result", func(t *testing.T) {
		buf := &bytes.Buffer{}
		reporter := NewConsoleReporter(buf, false)

		err := reporter.ReportSummary(nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be nil")
	})

	t.Run("result without errors", func(t *testing.T) {
		buf := &bytes.Buffer{}
		reporter := NewConsoleReporter(buf, false)

		result := &types.Result{
			StartTime: time.Now(),
			EndTime:   time.Now(),
			Duration:  5.0,
			Summary: &types.AggregatedMetrics{
				TotalOperations: 100,
				TotalErrors:     0,
				SuccessRate:     100.0,
				Throughput:      20.0,
				Latency: types.LatencyStats{
					Min:    10,
					Median: 15,
					P99:    30,
					Max:    40,
				},
			},
		}

		err := reporter.ReportSummary(result)
		require.NoError(t, err)

		output := buf.String()
		assert.NotContains(t, output, "Errors")
	})
}

func TestConsoleReporter_ReportJSON(t *testing.T) {
	t.Run("valid result", func(t *testing.T) {
		buf := &bytes.Buffer{}
		reporter := NewConsoleReporter(buf, false)

		result := &types.Result{
			StartTime: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			EndTime:   time.Date(2024, 1, 1, 12, 0, 10, 0, time.UTC),
			Duration:  10.0,
			Summary: &types.AggregatedMetrics{
				TotalOperations: 1000,
				SuccessRate:     99.5,
				Throughput:      100.0,
			},
		}

		err := reporter.ReportJSON(result)
		require.NoError(t, err)

		output := buf.String()
		assert.Contains(t, output, "\"total_operations\"")
		assert.Contains(t, output, "1000")
		assert.Contains(t, output, "99.5")
		assert.Contains(t, output, "100")
	})

	t.Run("nil result", func(t *testing.T) {
		buf := &bytes.Buffer{}
		reporter := NewConsoleReporter(buf, false)

		err := reporter.ReportJSON(nil)
		assert.Error(t, err)
	})
}

func TestConsoleReporter_Colors(t *testing.T) {
	t.Run("with colors enabled", func(t *testing.T) {
		buf := &bytes.Buffer{}
		reporter := NewConsoleReporter(buf, true)

		result := &types.Result{
			StartTime: time.Now(),
			EndTime:   time.Now(),
			Duration:  1.0,
			Summary: &types.AggregatedMetrics{
				TotalOperations: 100,
				SuccessRate:     99.0,
				Throughput:      100.0,
				Latency: types.LatencyStats{
					Min:    10,
					Median: 15,
					P99:    30,
					Max:    40,
				},
			},
		}

		err := reporter.ReportSummary(result)
		require.NoError(t, err)

		output := buf.String()
		// Should contain ANSI color codes
		assert.Contains(t, output, "\033[")
	})

	t.Run("with colors disabled", func(t *testing.T) {
		buf := &bytes.Buffer{}
		reporter := NewConsoleReporter(buf, false)

		result := &types.Result{
			StartTime: time.Now(),
			EndTime:   time.Now(),
			Duration:  1.0,
			Summary: &types.AggregatedMetrics{
				TotalOperations: 100,
				SuccessRate:     99.0,
				Throughput:      100.0,
				Latency: types.LatencyStats{
					Min:    10,
					Median: 15,
					P99:    30,
					Max:    40,
				},
			},
		}

		err := reporter.ReportSummary(result)
		require.NoError(t, err)

		output := buf.String()
		// Should NOT contain ANSI color codes
		assert.NotContains(t, output, "\033[")
	})
}

func TestConsoleReporter_FormatDuration(t *testing.T) {
	reporter := NewConsoleReporter(&bytes.Buffer{}, false)

	tests := []struct {
		duration time.Duration
		expected string
	}{
		{100 * time.Millisecond, "100ms"},
		{500 * time.Millisecond, "500ms"},
		{1 * time.Second, "1.0s"},
		{5*time.Second + 500*time.Millisecond, "5.5s"},
		{30 * time.Second, "30.0s"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := reporter.formatDuration(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConsoleReporter_FormatPercentage(t *testing.T) {
	reporterNoColor := NewConsoleReporter(&bytes.Buffer{}, false)

	tests := []struct {
		percentage float64
		contains   string
	}{
		{100.0, "100.00%"},
		{99.5, "99.50%"},
		{95.0, "95.00%"},
		{89.0, "89.00%"},
		{50.0, "50.00%"},
	}

	for _, tt := range tests {
		t.Run(tt.contains, func(t *testing.T) {
			result := reporterNoColor.formatPercentage(tt.percentage)
			assert.Equal(t, tt.contains, result)
		})
	}
}

func TestConsoleReporter_Colorize(t *testing.T) {
	t.Run("with colors", func(t *testing.T) {
		reporter := NewConsoleReporter(&bytes.Buffer{}, true)
		result := reporter.colorize("test", colorRed)
		assert.Contains(t, result, "\033[31m")
		assert.Contains(t, result, "test")
		assert.Contains(t, result, "\033[0m")
	})

	t.Run("without colors", func(t *testing.T) {
		reporter := NewConsoleReporter(&bytes.Buffer{}, false)
		result := reporter.colorize("test", colorRed)
		assert.Equal(t, "test", result)
	})
}

func TestCenterString(t *testing.T) {
	tests := []struct {
		input    string
		width    int
		expected string
	}{
		{"test", 10, "   test"},
		{"test", 8, "  test"},
		{"test", 4, "test"},
		{"test", 2, "test"},
		{"hello world", 20, "    hello world"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := centerString(tt.input, tt.width)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConsoleReporter_PrintOperationMetrics(t *testing.T) {
	buf := &bytes.Buffer{}
	reporter := NewConsoleReporter(buf, false)

	metrics := &types.AggregatedMetrics{
		TotalOperations: 500,
		SuccessRate:     98.5,
		Throughput:      50.0,
		Latency: types.LatencyStats{
			Min:    5,
			Median: 15,
			P99:    45,
			Max:    60,
		},
	}

	reporter.printOperationMetrics(metrics)

	output := buf.String()
	assert.Contains(t, output, "500")
	assert.Contains(t, output, "98.5")
	assert.Contains(t, output, "50.0")
	assert.Contains(t, output, "5ms")
	assert.Contains(t, output, "15ms")
	assert.Contains(t, output, "45ms")
	assert.Contains(t, output, "60ms")
}

func TestConsoleReporter_LowSuccessRate(t *testing.T) {
	buf := &bytes.Buffer{}
	reporter := NewConsoleReporter(buf, true)

	result := &types.Result{
		StartTime: time.Now(),
		EndTime:   time.Now(),
		Duration:  1.0,
		Summary: &types.AggregatedMetrics{
			TotalOperations: 100,
			TotalErrors:     20,
			SuccessRate:     80.0, // Low success rate should trigger red color
			Throughput:      100.0,
			Latency: types.LatencyStats{
				Min:    10,
				Median: 15,
				P99:    30,
				Max:    40,
			},
			ErrorsByType: map[string]int64{
				"error": 20,
			},
		},
	}

	err := reporter.ReportSummary(result)
	require.NoError(t, err)

	output := buf.String()
	// Should contain red color code for low success rate
	assert.Contains(t, output, "80.00%")
	assert.Contains(t, output, "Errors")
}
