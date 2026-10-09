// Package types defines the core types and data structures for the Syntrix benchmark tool.
package types

import (
	"encoding/json"
	"time"

	"github.com/codetreker/syntrix/pkg/model"
)

// UnixMilliTime wraps time.Time to support JSON unmarshaling from Unix millisecond timestamps.
type UnixMilliTime struct {
	time.Time
}

// UnmarshalJSON implements json.Unmarshaler for UnixMilliTime.
// It accepts both Unix millisecond timestamps (numbers) and RFC3339 strings.
func (t *UnixMilliTime) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as a number (Unix milliseconds)
	var millis int64
	if err := json.Unmarshal(data, &millis); err == nil {
		t.Time = time.UnixMilli(millis)
		return nil
	}

	// Fall back to standard time.Time unmarshaling (RFC3339 string)
	return json.Unmarshal(data, &t.Time)
}

// MarshalJSON implements json.Marshaler for UnixMilliTime.
// It outputs the time as Unix milliseconds.
func (t UnixMilliTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UnixMilli())
}

// Config holds the complete benchmark configuration.
type Config struct {
	// Benchmark metadata
	Name     string        `json:"name" yaml:"name"`
	Target   string        `json:"target" yaml:"target"`     // Target Syntrix URL
	Duration time.Duration `json:"duration" yaml:"duration"` // Total benchmark duration
	Warmup   time.Duration `json:"warmup" yaml:"warmup"`     // Warmup period
	Rampup   time.Duration `json:"rampup" yaml:"rampup"`     // Rampup period
	Workers  int           `json:"workers" yaml:"workers"`   // Number of concurrent workers

	// Authentication
	Auth AuthConfig `json:"auth" yaml:"auth"`

	// Scenario configuration
	Scenario ScenarioConfig `json:"scenario" yaml:"scenario"`

	// Data generation configuration
	Data DataConfig `json:"data" yaml:"data"`

	// Metrics configuration
	Metrics MetricsConfig `json:"metrics" yaml:"metrics"`

	// Monitoring configuration
	Monitor MonitorConfig `json:"monitor" yaml:"monitor"`

	// Output configuration
	Output OutputConfig `json:"output" yaml:"output"`

	// Base directory for resolving relative paths (not serialized)
	BaseDir string `json:"-" yaml:"-"`
}

// AuthConfig holds authentication configuration.
type AuthConfig struct {
	Database       string `json:"database" yaml:"database"`
	Token          string `json:"-" yaml:"-"`                               // Auto-generated at runtime, not configurable
	PrivateKeyFile string `json:"private_key_file" yaml:"private_key_file"` // Path to private key for token generation
}

// ScenarioConfig defines the benchmark scenario.
type ScenarioConfig struct {
	Type       string            `json:"type" yaml:"type"` // crud, query, realtime, mixed
	Operations []OperationConfig `json:"operations" yaml:"operations"`
}

// OperationConfig defines a single operation type in the scenario.
type OperationConfig struct {
	Type   string                 `json:"type" yaml:"type"`     // create, read, update, delete, query
	Weight int                    `json:"weight" yaml:"weight"` // Relative weight for mixed scenarios
	Rate   int                    `json:"rate" yaml:"rate"`     // Target operations per second
	Config map[string]interface{} `json:"config" yaml:"config"` // Operation-specific config
}

// DataConfig holds data generation configuration.
type DataConfig struct {
	DocumentSize     string `json:"documentSize" yaml:"document_size"`         // e.g., "1KB", "10KB"
	FieldsCount      int    `json:"fieldsCount" yaml:"fields_count"`           // Number of fields per document
	SeedData         int    `json:"seedData" yaml:"seed_data"`                 // Pre-populate N documents
	Cleanup          bool   `json:"cleanup" yaml:"cleanup"`                    // Clean up data after benchmark
	CollectionPrefix string `json:"collectionPrefix" yaml:"collection_prefix"` // Prefix for test collections
}

// MetricsConfig holds metrics collection configuration.
type MetricsConfig struct {
	Interval  time.Duration   `json:"interval" yaml:"interval"` // Progress reporting interval
	Histogram HistogramConfig `json:"histogram" yaml:"histogram"`
}

// HistogramConfig defines latency histogram buckets.
type HistogramConfig struct {
	Buckets []int `json:"buckets" yaml:"buckets"` // Bucket boundaries in milliseconds
}

// MonitorConfig holds monitoring server configuration.
type MonitorConfig struct {
	Enabled  bool          `json:"enabled" yaml:"enabled"`
	Address  string        `json:"address" yaml:"address"`
	AutoOpen bool          `json:"autoOpen" yaml:"auto_open"`
	Interval time.Duration `json:"interval" yaml:"interval"` // Metrics update interval
}

// OutputConfig holds output configuration.
type OutputConfig struct {
	Console bool   `json:"console" yaml:"console"`
	File    string `json:"file" yaml:"file"`     // Output file path template
	Format  string `json:"format" yaml:"format"` // json, prometheus, csv
}

// Result holds the complete benchmark result.
type Result struct {
	// Session information
	SessionID string    `json:"session_id"`
	Name      string    `json:"name"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Duration  float64   `json:"duration_seconds"`

	// Configuration snapshot
	Config *Config `json:"config"`

	// Summary metrics
	Summary *AggregatedMetrics `json:"summary"`

	// Per-operation metrics
	Operations map[string]*AggregatedMetrics `json:"operations"`

	// Error details
	Errors []ErrorEntry `json:"errors"`

	// Timeline data (optional)
	Timeline []TimelinePoint `json:"timeline,omitempty"`
}

// OperationResult represents the result of a single operation execution.
type OperationResult struct {
	OperationType string        // Operation type (create, read, update, delete, query)
	StartTime     time.Time     // Operation start time
	Duration      time.Duration // Operation duration
	Success       bool          // Whether the operation succeeded
	Error         error         // Error if operation failed
	StatusCode    int           // HTTP status code (if applicable)
	BytesSent     int64         // Bytes sent in request
	BytesReceived int64         // Bytes received in response
}

// AggregatedMetrics holds aggregated statistical metrics.
type AggregatedMetrics struct {
	// Counts
	TotalOperations int64   `json:"total_operations"`
	TotalErrors     int64   `json:"total_errors"`
	SuccessRate     float64 `json:"success_rate"`

	// Throughput
	Throughput float64 `json:"throughput"` // Operations per second

	// Latency statistics (in milliseconds)
	Latency LatencyStats `json:"latency"`

	// Error distribution
	ErrorsByType map[string]int64 `json:"errors_by_type,omitempty"`
}

// LatencyStats holds latency statistics.
type LatencyStats struct {
	Min    int64   `json:"min"`    // Minimum latency (ms)
	Max    int64   `json:"max"`    // Maximum latency (ms)
	Mean   float64 `json:"mean"`   // Mean latency (ms)
	Median int64   `json:"median"` // Median latency (ms)
	P90    int64   `json:"p90"`    // 90th percentile (ms)
	P95    int64   `json:"p95"`    // 95th percentile (ms)
	P99    int64   `json:"p99"`    // 99th percentile (ms)
}

// ErrorEntry represents a single error occurrence.
type ErrorEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Operation string    `json:"operation"`
	WorkerID  string    `json:"worker_id,omitempty"`
}

// TimelinePoint represents metrics at a specific point in time.
type TimelinePoint struct {
	Timestamp  time.Time `json:"timestamp"`
	Elapsed    float64   `json:"elapsed_seconds"`
	Throughput float64   `json:"throughput"`
	LatencyP99 int64     `json:"latency_p99"`
	ErrorRate  float64   `json:"error_rate"`
}

// TestEnv provides access to the test environment for scenarios.
type TestEnv struct {
	Client  Client
	Config  *Config
	BaseURL string
	Token   string
}

// Document represents a Syntrix document.
type Document struct {
	ID         string                 `json:"id"`
	Collection string                 `json:"collection,omitempty"`
	Data       map[string]interface{} `json:"data,omitempty"`
	Version    int64                  `json:"version,omitempty"`
	CreatedAt  UnixMilliTime          `json:"createdAt,omitempty"`
	UpdatedAt  UnixMilliTime          `json:"updatedAt,omitempty"`
}

// Query represents a Syntrix query.
type Query struct {
	Collection string         `json:"collection"`
	Filters    []model.Filter `json:"filters,omitempty"`
	OrderBy    []model.Order  `json:"order_by,omitempty"`
	Limit      int            `json:"limit,omitempty"`
	Offset     int            `json:"offset,omitempty"`
}

// Subscription represents a realtime subscription.
type Subscription struct {
	ID      string
	Query   Query
	Events  chan *SubscriptionEvent
	Errors  chan error
	closeFn func() error
}

// Close closes the subscription.
func (s *Subscription) Close() error {
	if s.closeFn != nil {
		return s.closeFn()
	}
	return nil
}

// SubscriptionEvent represents a realtime event.
type SubscriptionEvent struct {
	Type     string // "create", "update", "delete"
	Document *Document
}

// BenchmarkStatus represents the current status of the benchmark.
type BenchmarkStatus string

const (
	StatusStarting  BenchmarkStatus = "starting"
	StatusWarmup    BenchmarkStatus = "warmup"
	StatusRamping   BenchmarkStatus = "ramping"
	StatusRunning   BenchmarkStatus = "running"
	StatusCompleted BenchmarkStatus = "completed"
	StatusFailed    BenchmarkStatus = "failed"
	StatusCancelled BenchmarkStatus = "cancelled"
)

// WorkerStatus represents the status of a single worker.
type WorkerStatus struct {
	ID                  string      `json:"id"`
	Status              string      `json:"status"` // active, idle, stopped
	OperationsCompleted int64       `json:"operations_completed"`
	CurrentOperation    string      `json:"current_operation,omitempty"`
	LastError           *ErrorEntry `json:"last_error,omitempty"`
}
