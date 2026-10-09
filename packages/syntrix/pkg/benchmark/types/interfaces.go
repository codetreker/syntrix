// Package types defines the core types and interfaces for the Syntrix benchmark tool.
package types

import (
	"context"
)

// Runner orchestrates the entire benchmark execution lifecycle.
type Runner interface {
	// Initialize prepares the benchmark environment
	Initialize(ctx context.Context, config *Config) error

	// Run executes the benchmark scenario
	Run(ctx context.Context) (*Result, error)

	// Cleanup performs post-benchmark cleanup
	Cleanup(ctx context.Context) error
}

// Worker executes operations concurrently according to the scenario definition.
type Worker interface {
	// Start begins worker execution
	Start(ctx context.Context) error

	// Execute performs a single operation
	Execute(ctx context.Context, op Operation) (*OperationResult, error)

	// Stop gracefully stops the worker
	Stop(ctx context.Context) error
}

// Scenario defines a benchmark scenario (CRUD, Query, Realtime, etc.).
type Scenario interface {
	// Name returns the scenario identifier
	Name() string

	// Setup prepares scenario prerequisites (e.g., seed data)
	Setup(ctx context.Context, env *TestEnv) error

	// NextOperation returns the next operation to execute
	NextOperation() (Operation, error)

	// Teardown cleans up scenario resources
	Teardown(ctx context.Context, env *TestEnv) error
}

// Operation represents a single benchmark operation (create, read, update, delete, query, etc.).
type Operation interface {
	// Type returns the operation type (e.g., "create", "read", "update", "delete", "query")
	Type() string

	// Execute performs the operation and returns the result
	Execute(ctx context.Context, client Client) (*OperationResult, error)
}

// Client abstracts HTTP, WebSocket, and SSE communication with Syntrix.
type Client interface {
	// HTTP operations
	CreateDocument(ctx context.Context, collection string, doc map[string]interface{}) (*Document, error)
	GetDocument(ctx context.Context, collection, id string) (*Document, error)
	UpdateDocument(ctx context.Context, collection, id string, doc map[string]interface{}) (*Document, error)
	DeleteDocument(ctx context.Context, collection, id string) error
	Query(ctx context.Context, query Query) ([]*Document, error)

	// Realtime operations
	Subscribe(ctx context.Context, query Query) (*Subscription, error)
	Unsubscribe(ctx context.Context, subID string) error

	// Close closes the client and releases resources
	Close() error
}

// MetricsCollector collects and aggregates benchmark metrics in real-time.
type MetricsCollector interface {
	// RecordOperation records the result of a single operation
	RecordOperation(result *OperationResult)

	// GetMetrics returns current aggregated metrics
	GetMetrics() *AggregatedMetrics

	// GetSnapshot returns a snapshot of current metrics (thread-safe copy)
	GetSnapshot() *AggregatedMetrics

	// Reset resets all metrics
	Reset()
}
