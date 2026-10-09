// Package reporter provides output formatting for benchmark results.
package reporter

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
)

// ConsoleReporter outputs benchmark results to the console.
type ConsoleReporter struct {
	writer io.Writer
	colors bool
}

// NewConsoleReporter creates a new console reporter.
func NewConsoleReporter(writer io.Writer, colors bool) *ConsoleReporter {
	return &ConsoleReporter{
		writer: writer,
		colors: colors,
	}
}

// ReportProgress reports real-time progress during benchmark execution.
func (r *ConsoleReporter) ReportProgress(elapsed time.Duration, metrics *types.AggregatedMetrics) {
	if metrics == nil {
		return
	}

	fmt.Fprintf(r.writer, "\r[%s] Ops: %d | Success: %.1f%% | Throughput: %.2f ops/s | P99: %dms",
		r.formatDuration(elapsed),
		metrics.TotalOperations,
		metrics.SuccessRate,
		metrics.Throughput,
		metrics.Latency.P99,
	)
}

// ReportSummary reports the final benchmark summary.
func (r *ConsoleReporter) ReportSummary(result *types.Result) error {
	if result == nil {
		return fmt.Errorf("result cannot be nil")
	}

	fmt.Fprintln(r.writer)
	r.printHeader("Benchmark Results")
	fmt.Fprintln(r.writer)

	// Session info
	r.printSection("Session Information")
	fmt.Fprintf(r.writer, "  Duration:  %s\n", r.formatDuration(time.Duration(result.Duration*float64(time.Second))))
	fmt.Fprintf(r.writer, "  Started:   %s\n", result.StartTime.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(r.writer, "  Finished:  %s\n", result.EndTime.Format("2006-01-02 15:04:05"))
	fmt.Fprintln(r.writer)

	// Summary metrics
	if result.Summary != nil {
		r.printSection("Overall Performance")
		r.printMetrics(result.Summary)
		fmt.Fprintln(r.writer)
	}

	// Per-operation metrics
	if len(result.Operations) > 0 {
		r.printSection("Per-Operation Metrics")
		for opType, metrics := range result.Operations {
			fmt.Fprintf(r.writer, "  %s:\n", r.colorize(strings.ToUpper(opType), colorCyan))
			r.printOperationMetrics(metrics)
			fmt.Fprintln(r.writer)
		}
	}

	// Errors
	if result.Summary != nil && result.Summary.TotalErrors > 0 {
		r.printSection("Errors")
		fmt.Fprintf(r.writer, "  Total Errors: %s\n", r.colorize(fmt.Sprintf("%d", result.Summary.TotalErrors), colorRed))
		if len(result.Summary.ErrorsByType) > 0 {
			fmt.Fprintln(r.writer, "  By Type:")
			for errType, count := range result.Summary.ErrorsByType {
				fmt.Fprintf(r.writer, "    - %s: %d\n", errType, count)
			}
		}
		fmt.Fprintln(r.writer)
	}

	r.printFooter()
	return nil
}

// ReportJSON outputs results in JSON format.
func (r *ConsoleReporter) ReportJSON(result *types.Result) error {
	if result == nil {
		return fmt.Errorf("result cannot be nil")
	}

	encoder := json.NewEncoder(r.writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

// printMetrics prints aggregated metrics.
func (r *ConsoleReporter) printMetrics(metrics *types.AggregatedMetrics) {
	fmt.Fprintf(r.writer, "  Total Operations:  %s\n", r.formatNumber(metrics.TotalOperations))
	fmt.Fprintf(r.writer, "  Success Rate:      %s\n", r.formatPercentage(metrics.SuccessRate))
	fmt.Fprintf(r.writer, "  Throughput:        %s\n", r.formatThroughput(metrics.Throughput))
	fmt.Fprintln(r.writer)

	fmt.Fprintf(r.writer, "  Latency:\n")
	fmt.Fprintf(r.writer, "    Min:     %s\n", r.formatLatency(metrics.Latency.Min))
	fmt.Fprintf(r.writer, "    Median:  %s\n", r.formatLatency(metrics.Latency.Median))
	fmt.Fprintf(r.writer, "    Mean:    %s\n", r.formatLatency(int64(metrics.Latency.Mean)))
	fmt.Fprintf(r.writer, "    Max:     %s\n", r.formatLatency(metrics.Latency.Max))
	fmt.Fprintf(r.writer, "    P90:     %s\n", r.formatLatency(metrics.Latency.P90))
	fmt.Fprintf(r.writer, "    P95:     %s\n", r.formatLatency(metrics.Latency.P95))
	fmt.Fprintf(r.writer, "    P99:     %s\n", r.formatLatency(metrics.Latency.P99))
}

// printOperationMetrics prints per-operation metrics.
func (r *ConsoleReporter) printOperationMetrics(metrics *types.AggregatedMetrics) {
	fmt.Fprintf(r.writer, "    Operations: %d | Success: %.1f%% | Throughput: %.2f ops/s\n",
		metrics.TotalOperations,
		metrics.SuccessRate,
		metrics.Throughput,
	)
	fmt.Fprintf(r.writer, "    Latency: Min=%dms | Median=%dms | P99=%dms | Max=%dms\n",
		metrics.Latency.Min,
		metrics.Latency.Median,
		metrics.Latency.P99,
		metrics.Latency.Max,
	)
}

// printHeader prints a section header.
func (r *ConsoleReporter) printHeader(title string) {
	line := strings.Repeat("=", 70)
	fmt.Fprintln(r.writer, r.colorize(line, colorBold))
	fmt.Fprintf(r.writer, "%s\n", r.colorize(centerString(title, 70), colorBold))
	fmt.Fprintln(r.writer, r.colorize(line, colorBold))
}

// printSection prints a section title.
func (r *ConsoleReporter) printSection(title string) {
	fmt.Fprintf(r.writer, "%s\n", r.colorize(title, colorBold))
}

// printFooter prints the footer.
func (r *ConsoleReporter) printFooter() {
	line := strings.Repeat("=", 70)
	fmt.Fprintln(r.writer, r.colorize(line, colorBold))
}

// Formatting helpers

func (r *ConsoleReporter) formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func (r *ConsoleReporter) formatNumber(n int64) string {
	return r.colorize(fmt.Sprintf("%d", n), colorGreen)
}

func (r *ConsoleReporter) formatPercentage(p float64) string {
	color := colorGreen
	if p < 95.0 {
		color = colorYellow
	}
	if p < 90.0 {
		color = colorRed
	}
	return r.colorize(fmt.Sprintf("%.2f%%", p), color)
}

func (r *ConsoleReporter) formatThroughput(t float64) string {
	return r.colorize(fmt.Sprintf("%.2f ops/s", t), colorGreen)
}

func (r *ConsoleReporter) formatLatency(l int64) string {
	return fmt.Sprintf("%dms", l)
}

// Color codes
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

func (r *ConsoleReporter) colorize(text, color string) string {
	if !r.colors {
		return text
	}
	return color + text + colorReset
}

func centerString(s string, width int) string {
	if len(s) >= width {
		return s
	}
	padding := (width - len(s)) / 2
	return strings.Repeat(" ", padding) + s
}
