package grpc

import (
	"context"
	"errors"
	"fmt"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Service defines the interface for the Query Engine.
// This is a copy of query.Service to avoid circular imports.
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

// Server implements the gRPC QueryServiceServer interface.
// It wraps a query.Service and handles proto conversion.
type Server struct {
	pb.UnimplementedQueryServiceServer
	service Service
}

// NewServer creates a new gRPC server adapter.
func NewServer(service Service) *Server {
	return &Server{service: service}
}

// GetDocument retrieves a document by its path.
func (s *Server) GetDocument(ctx context.Context, req *pb.GetDocumentRequest) (*pb.GetDocumentResponse, error) {
	doc, err := s.service.GetDocument(ctx, req.Database, req.Path)
	if err != nil {
		return nil, errorToStatus(err)
	}
	return &pb.GetDocumentResponse{
		Document: modelDocToProto(doc),
	}, nil
}

// CreateDocument creates a new document.
func (s *Server) CreateDocument(ctx context.Context, req *pb.CreateDocumentRequest) (*pb.CreateDocumentResponse, error) {
	doc := protoToModelDoc(req.Document)
	err := s.service.CreateDocument(ctx, req.Database, doc)
	if err != nil {
		return nil, errorToStatus(err)
	}
	return &pb.CreateDocumentResponse{}, nil
}

// ReplaceDocument replaces an existing document with optional filters.
func (s *Server) ReplaceDocument(ctx context.Context, req *pb.ReplaceDocumentRequest) (*pb.ReplaceDocumentResponse, error) {
	doc := protoToModelDoc(req.Document)
	filters := protoToFilters(req.Filters)

	result, err := s.service.ReplaceDocument(ctx, req.Database, doc, filters)
	if err != nil {
		return nil, errorToStatus(err)
	}
	return &pb.ReplaceDocumentResponse{
		Document: modelDocToProto(result),
	}, nil
}

// PatchDocument partially updates an existing document with optional filters.
func (s *Server) PatchDocument(ctx context.Context, req *pb.PatchDocumentRequest) (*pb.PatchDocumentResponse, error) {
	doc := protoToModelDoc(req.Document)
	filters := protoToFilters(req.Filters)

	result, err := s.service.PatchDocument(ctx, req.Database, doc, filters)
	if err != nil {
		return nil, errorToStatus(err)
	}
	return &pb.PatchDocumentResponse{
		Document: modelDocToProto(result),
	}, nil
}

// DeleteDocument removes a document by its path with optional filters.
func (s *Server) DeleteDocument(ctx context.Context, req *pb.DeleteDocumentRequest) (*pb.DeleteDocumentResponse, error) {
	filters := protoToFilters(req.Filters)

	err := s.service.DeleteDocument(ctx, req.Database, req.Path, filters)
	if err != nil {
		return nil, errorToStatus(err)
	}
	return &pb.DeleteDocumentResponse{}, nil
}

// ExecuteQuery executes a query and returns matching documents.
func (s *Server) ExecuteQuery(ctx context.Context, req *pb.ExecuteQueryRequest) (*pb.ExecuteQueryResponse, error) {
	ctx = ctxkeys.IncomingRequestContext(ctx)
	if req.WireVersion != wire.Version {
		return nil, queryErrorToStatus(fmt.Errorf("%w: unsupported query wire version", model.ErrInvalidQuery))
	}
	q, err := wire.DecodeQuery(req.Query)
	if err != nil {
		return nil, queryErrorToStatus(err)
	}
	service, ok := s.service.(interface {
		ExecuteQueryPage(context.Context, string, model.Query) (model.QueryPage, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "query page service is required")
	}
	page, err := service.ExecuteQueryPage(ctx, req.Database, q)
	if err != nil {
		return nil, queryErrorToStatus(err)
	}
	response, err := wire.EncodePage(page)
	if err != nil {
		return nil, queryErrorToStatus(err)
	}
	return response, nil
}

func queryErrorToStatus(err error) error {
	converted := errorToStatus(err)
	if status.Code(converted) == codes.Internal {
		return status.Error(codes.Internal, "query execution failed")
	}
	return converted
}

// Pull retrieves documents for replication.
func (s *Server) Pull(ctx context.Context, req *pb.PullRequest) (*pb.PullResponse, error) {
	pullReq := protoToPullRequest(req)

	resp, err := s.service.Pull(ctx, req.Database, pullReq)
	if err != nil {
		return nil, errorToStatus(err)
	}

	return pullResponseToProto(resp), nil
}

// Push sends documents for replication.
func (s *Server) Push(ctx context.Context, req *pb.PushRequest) (*pb.PushResponse, error) {
	pushReq := protoToPushRequest(req)

	resp, err := s.service.Push(ctx, req.Database, pushReq)
	if err != nil {
		return nil, errorToStatus(err)
	}

	return pushResponseToProto(resp), nil
}

// ============================================================================
// Error handling
// ============================================================================

// errorToStatus converts domain errors to gRPC status.
func errorToStatus(err error) error {
	if err == nil {
		return nil
	}

	reasons := []struct {
		err    error
		code   codes.Code
		reason string
	}{
		{model.ErrStaleCursor, codes.FailedPrecondition, "STALE_CURSOR"},
		{model.ErrQueryWorkLimit, codes.ResourceExhausted, "QUERY_WORK_LIMIT"},
		{indexer.ErrNoMatchingIndex, codes.FailedPrecondition, "NO_MATCHING_INDEX"},
		{indexer.ErrIndexNotReady, codes.Unavailable, "INDEX_UNAVAILABLE"},
		{indexer.ErrIndexRebuilding, codes.Unavailable, "INDEX_UNAVAILABLE"},
	}
	for _, item := range reasons {
		if errors.Is(err, item.err) {
			st := status.New(item.code, item.err.Error())
			detailed, detailErr := st.WithDetails(&errdetails.ErrorInfo{Reason: item.reason, Domain: "syntrix.query"})
			if detailErr != nil {
				return st.Err()
			}
			return detailed.Err()
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "query deadline exceeded")
	}
	// Check for known error types
	if errors.Is(err, model.ErrNotFound) {
		return status.Error(codes.NotFound, err.Error())
	}
	if errors.Is(err, model.ErrPreconditionFailed) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if errors.Is(err, model.ErrExists) {
		return status.Error(codes.AlreadyExists, err.Error())
	}
	if errors.Is(err, model.ErrInvalidQuery) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if errors.Is(err, model.ErrPermissionDenied) {
		return status.Error(codes.PermissionDenied, err.Error())
	}
	if model.IsCanceled(err) {
		return status.Error(codes.Canceled, "operation canceled")
	}

	// Default to internal error
	return status.Error(codes.Internal, err.Error())
}

// statusToError converts gRPC status to domain errors.
func statusToError(err error) error {
	if err == nil {
		return nil
	}

	st, ok := status.FromError(err)
	if !ok {
		return err
	}

	switch st.Code() {
	case codes.NotFound:
		return model.ErrNotFound
	case codes.FailedPrecondition:
		return model.ErrPreconditionFailed
	case codes.AlreadyExists:
		return model.ErrExists
	case codes.InvalidArgument:
		return model.ErrInvalidQuery
	case codes.PermissionDenied:
		return model.ErrPermissionDenied
	default:
		return errors.New(st.Message())
	}
}
