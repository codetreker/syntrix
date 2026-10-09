package indexer

import (
	"log/slog"

	indexerv1 "github.com/codetreker/syntrix/api/gen/indexer/v1"
	"github.com/codetreker/syntrix/internal/indexer/client"
	internalgrpc "github.com/codetreker/syntrix/internal/indexer/grpc"
)

// NewGRPCServer creates a gRPC server adapter for the Indexer.
// The returned server implements indexerv1.IndexerServiceServer.
func NewGRPCServer(svc LocalService) indexerv1.IndexerServiceServer {
	return internalgrpc.NewServer(svc)
}

// Client is a gRPC client for the Indexer service.
type Client = client.Client

// NewClient creates a new gRPC client for the Indexer service.
func NewClient(address string, logger *slog.Logger) (*Client, error) {
	return client.New(address, logger)
}

// IndexerState contains the complete indexer state.
type IndexerState = client.IndexerState

// IndexSpec describes a desired index from configuration.
type IndexSpec = client.IndexSpec

// IndexInfo describes an actual index in memory.
type IndexInfo = client.IndexInfo

// IndexField describes a field in an index.
type IndexField = client.IndexField

// PendingOperation describes a reconciler operation.
type PendingOperation = client.PendingOperation

// ReloadResult contains the result of a reload operation.
type ReloadResult = client.ReloadResult
