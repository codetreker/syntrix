package client

import (
	"context"
	"errors"
	"fmt"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Client is a gRPC client for the Query Service.
type Client struct {
	conn   *grpc.ClientConn
	client pb.QueryServiceClient
}

// newClientFunc is the function used to create a gRPC client connection.
// This is a package-level variable to allow testing.
var newClientFunc = grpc.NewClient

// New creates a new Query Service gRPC Client.
func New(address string) (*Client, error) {
	return NewWithOptions(address)
}

// NewWithOptions creates a new Query Service gRPC Client with additional options.
func NewWithOptions(address string, opts ...grpc.DialOption) (*Client, error) {
	defaultOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(wire.MaxGRPCBytes))}
	allOpts := append(defaultOpts, opts...)

	conn, err := newClientFunc(address, allOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to query service: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewQueryServiceClient(conn),
	}, nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// GetDocument retrieves a document by path.
func (c *Client) GetDocument(ctx context.Context, database string, path string) (model.Document, error) {
	resp, err := c.client.GetDocument(ctx, &pb.GetDocumentRequest{
		Database: database,
		Path:     path,
	})
	if err != nil {
		return nil, statusToError(err)
	}

	return protoToModelDoc(resp.Document), nil
}

// CreateDocument creates a new document.
func (c *Client) CreateDocument(ctx context.Context, database string, doc model.Document) error {
	_, err := c.client.CreateDocument(ctx, &pb.CreateDocumentRequest{
		Database: database,
		Document: modelDocToProto(doc),
	})
	if err != nil {
		return statusToError(err)
	}
	return nil
}

// ReplaceDocument replaces a document with optional filters.
func (c *Client) ReplaceDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	resp, err := c.client.ReplaceDocument(ctx, &pb.ReplaceDocumentRequest{
		Database: database,
		Document: modelDocToProto(data),
		Filters:  filtersToProto(pred),
	})
	if err != nil {
		return nil, statusToError(err)
	}

	return protoToModelDoc(resp.Document), nil
}

// PatchDocument partially updates a document with optional filters.
func (c *Client) PatchDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	resp, err := c.client.PatchDocument(ctx, &pb.PatchDocumentRequest{
		Database: database,
		Document: modelDocToProto(data),
		Filters:  filtersToProto(pred),
	})
	if err != nil {
		return nil, statusToError(err)
	}

	return protoToModelDoc(resp.Document), nil
}

// DeleteDocument removes a document by path with optional filters.
func (c *Client) DeleteDocument(ctx context.Context, database string, path string, pred model.Filters) error {
	_, err := c.client.DeleteDocument(ctx, &pb.DeleteDocumentRequest{
		Database: database,
		Path:     path,
		Filters:  filtersToProto(pred),
	})
	if err != nil {
		return statusToError(err)
	}
	return nil
}

// ExecuteQuery executes a query and returns matching documents.
func (c *Client) ExecuteQuery(ctx context.Context, database string, q model.Query) ([]model.Document, error) {
	page, err := c.ExecuteQueryPage(ctx, database, q)
	return page.Documents, err
}

func (c *Client) ExecuteQueryPage(ctx context.Context, database string, q model.Query) (model.QueryPage, error) {
	encoded, err := wire.EncodeQuery(q)
	if err != nil {
		return model.QueryPage{}, err
	}
	response, err := c.client.ExecuteQuery(ctxkeys.OutgoingRequestContext(ctx), &pb.ExecuteQueryRequest{Database: database, Query: encoded, WireVersion: wire.Version})
	if err != nil {
		return model.QueryPage{}, statusToError(err)
	}
	return wire.DecodePage(response)
}

// Pull retrieves documents for replication.
func (c *Client) Pull(ctx context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	resp, err := c.client.Pull(ctx, &pb.PullRequest{
		Database:   database,
		Collection: req.Collection,
		Checkpoint: req.Checkpoint,
		Limit:      int32(req.Limit),
	})
	if err != nil {
		return nil, statusToError(err)
	}

	docs := make([]*storage.StoredDoc, 0, len(resp.Documents))
	for _, d := range resp.Documents {
		docs = append(docs, protoToStoredDoc(d))
	}

	return &storage.ReplicationPullResponse{
		Documents:  docs,
		Checkpoint: resp.Checkpoint,
	}, nil
}

// Push sends documents for replication.
func (c *Client) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	changes := make([]*pb.PushChange, 0, len(req.Changes))
	for _, change := range req.Changes {
		changes = append(changes, pushChangeToProto(change))
	}

	resp, err := c.client.Push(ctx, &pb.PushRequest{
		Database:   database,
		Collection: req.Collection,
		Changes:    changes,
	})
	if err != nil {
		return nil, statusToError(err)
	}

	conflicts := make([]*storage.StoredDoc, 0, len(resp.Conflicts))
	for _, d := range resp.Conflicts {
		conflicts = append(conflicts, protoToStoredDoc(d))
	}

	return &storage.ReplicationPushResponse{
		Conflicts: conflicts,
	}, nil
}

// ============================================================================
// Error handling
// ============================================================================

type invalidQueryError struct {
	message string
}

func (e invalidQueryError) Error() string {
	return e.message
}

func (e invalidQueryError) Unwrap() error {
	return model.ErrInvalidQuery
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

	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Domain == "syntrix.query" {
			switch info.Reason {
			case "STALE_CURSOR":
				return model.ErrStaleCursor
			case "QUERY_WORK_LIMIT":
				return model.ErrQueryWorkLimit
			case "NO_MATCHING_INDEX":
				return indexer.ErrNoMatchingIndex
			case "INDEX_UNAVAILABLE":
				return indexer.ErrIndexNotReady
			}
		}
	}
	switch st.Code() {
	case codes.Canceled:
		return context.Canceled
	case codes.DeadlineExceeded:
		return context.DeadlineExceeded
	case codes.ResourceExhausted:
		return model.ErrQueryWorkLimit
	case codes.NotFound:
		return model.ErrNotFound
	case codes.FailedPrecondition:
		return model.ErrPreconditionFailed
	case codes.AlreadyExists:
		return model.ErrExists
	case codes.InvalidArgument:
		return invalidQueryError{message: st.Message()}
	case codes.PermissionDenied:
		return model.ErrPermissionDenied
	default:
		return errors.New(st.Message())
	}
}
