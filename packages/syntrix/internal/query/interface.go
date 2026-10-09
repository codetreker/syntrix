package query

import (
	"context"

	pb "github.com/codetreker/syntrix/api/gen/query/v1"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/indexer"
	"github.com/codetreker/syntrix/internal/query/client"
	"github.com/codetreker/syntrix/internal/query/core"
	"github.com/codetreker/syntrix/internal/query/grpc"
	"github.com/codetreker/syntrix/pkg/model"
)

// Service defines the interface for the Query Engine.
// Both the local Engine and the remote Client implement this interface.
type Service interface {
	GetDocument(ctx context.Context, database string, path string) (model.Document, error)
	CreateDocument(ctx context.Context, database string, doc model.Document) error
	ReplaceDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error)
	PatchDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error)
	DeleteDocument(ctx context.Context, database string, path string, pred model.Filters) error
	ExecuteQuery(ctx context.Context, database string, q model.Query) ([]model.Document, error)
	Pull(ctx context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error)
	Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error)
}

// NewService creates a new local Query Service.
func NewService(store storage.DocumentStore, idx indexer.Service) Service {
	return core.New(store, idx)
}

// NewClient creates a new remote Query Service client (gRPC client).
// Use this when the query service is running remotely.
func NewClient(address string) (Service, error) {
	return client.New(address)
}

// NewGRPCServer creates a gRPC server for the Query Service.
// The server implements pb.QueryServiceServer and wraps the Service interface.
func NewGRPCServer(s Service) pb.QueryServiceServer {
	return grpc.NewServer(s)
}

// ============================================================================
// Deprecated type aliases and functions - for backward compatibility.
// These will be removed in a future release.
// ============================================================================

// Engine is a deprecated alias. Use NewService instead.
// Deprecated: Use NewService to get a Service interface.
type Engine = core.Engine
