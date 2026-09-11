// Package indexer provides the Indexer service for maintaining secondary indexes.
//
// The Indexer service subscribes to change events from the Puller service and
// maintains in-memory btree indexes for fast query execution. It provides:
//
//   - Template-based indexing: Define indexes via YAML configuration
//   - Query-to-Index matching: Automatically select the best index for queries
//   - Pagination support: Cursor-based pagination with OrderKey encoding
//   - Multi-database isolation: Separate index namespaces per database
//
// # Usage
//
//	cfg := indexer.Config{TemplatePath: "config/templates.yaml"}
//	svc := indexer.NewService(cfg, pullerSvc, logger)
//	svc.Start(ctx)
//
//	// Search using the index
//	results, err := svc.Search(ctx, "mydb", plan)
package indexer

import (
	"context"

	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
)

// Service defines the interface for the Indexer service.
type Service interface {
	// Search returns access candidates in the plan's common order. Query must
	// materialize source documents and evaluate every predicate before returning
	// user-visible results; an index reference alone is not a verified match.
	Search(ctx context.Context, database string, plan Plan) ([]DocRef, error)

	// Health returns current health status of the indexer.
	Health(ctx context.Context) (Health, error)

	// Stats returns aggregate readings for this service instance. Fields are sampled
	// independently; event counters reset when a new instance is created.
	Stats(ctx context.Context) (Stats, error)
}

// CandidateService opens a request-owned ordered stream for Query materialization.
// Callers must close the stream on page completion, error, or cancellation.
type CandidateService interface {
	OpenCandidates(ctx context.Context, database string, plan Plan) (manager.CandidateStream, error)
}

type CandidateStream = manager.CandidateStream
type CandidateGroup = manager.CandidateGroup
type CandidateMetadata = manager.CandidateMetadata

// LocalService extends Service with methods for managing the indexer lifecycle.
type LocalService interface {
	Service

	// Start starts the indexer service, including Puller subscription.
	Start(ctx context.Context) error

	// Stop gracefully stops the indexer service.
	Stop(ctx context.Context) error

	// ApplyEvent applies a single change event to the indexes.
	// The progress marker is saved atomically with the index updates.
	// This is called by the Puller subscription handler.
	ApplyEvent(ctx context.Context, evt *ChangeEvent, progress string) error

	// InvalidateDatabase removes all index entries for a database.
	// Used when a database is being deleted to clean up index state.
	InvalidateDatabase(ctx context.Context, database string) error

	// Manager returns the underlying index manager for advanced operations.
	Manager() *manager.Manager
}

// Plan represents a query plan passed from Query Engine.
type Plan = manager.Plan

// Filter represents a query filter on a field.
type Filter = manager.Filter

// FilterOp represents the type of filter operation.
type FilterOp = manager.FilterOp

// Filter operation constants.
const (
	FilterEq       = manager.FilterEq
	FilterGt       = manager.FilterGt
	FilterLt       = manager.FilterLt
	FilterGte      = manager.FilterGte
	FilterLte      = manager.FilterLte
	FilterNe       = manager.FilterNe
	FilterIn       = manager.FilterIn
	FilterContains = manager.FilterContains
)

// Direction represents sort direction.
type Direction = encoding.Direction

// Direction constants.
const (
	Asc  = encoding.Asc
	Desc = encoding.Desc
)

// OrderField represents an ordering specification.
type OrderField = manager.OrderField

// DocRef represents a document reference with its OrderKey.
type DocRef = manager.DocRef

// Health represents the health status of the indexer.
type Health = manager.Health

// HealthStatus represents the health status.
type HealthStatus = manager.HealthStatus

const (
	HealthOK        = manager.HealthOK
	HealthDegraded  = manager.HealthDegraded
	HealthUnhealthy = manager.HealthUnhealthy
)

// Stats represents index statistics.
type Stats = manager.Stats

// IndexManager is the underlying manager type. Exported for testing and advanced operations.
type IndexManager = manager.Manager

// ChangeEvent is the change event type from Puller.
type ChangeEvent = manager.ChangeEvent

// Indexer errors - exported for use by Query Engine and other consumers.
var (
	// ErrNoMatchingIndex is returned when no index template matches the query.
	ErrNoMatchingIndex = manager.ErrNoMatchingIndex

	// ErrIndexNotReady is returned when the index exists but is not ready to serve queries.
	ErrIndexNotReady = manager.ErrIndexNotReady

	// ErrIndexRebuilding is returned when the index is currently being rebuilt.
	ErrIndexRebuilding = manager.ErrIndexRebuilding

	// ErrInvalidPlan is returned when the query plan is invalid.
	ErrInvalidPlan = manager.ErrInvalidPlan
)
