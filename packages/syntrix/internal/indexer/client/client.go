// Package client provides a gRPC client for the Indexer service.
package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"

	indexerv1 "github.com/codetreker/syntrix/api/gen/indexer/v1"
	"github.com/codetreker/syntrix/internal/indexer/encoding"
	"github.com/codetreker/syntrix/internal/indexer/manager"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// grpcDialer is the function used to create gRPC connections.
// It can be overridden in tests to simulate dial failures.
var grpcDialer = func(target string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	return grpc.NewClient(target, opts...)
}

// Client implements the indexer.Service interface over gRPC.
type Client struct {
	conn   *grpc.ClientConn
	client indexerv1.IndexerServiceClient
	logger *slog.Logger
}

// New creates a new gRPC client for the Indexer service.
func New(address string, logger *slog.Logger) (*Client, error) {
	conn, err := grpcDialer(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to indexer: %w", err)
	}

	return &Client{
		conn:   conn,
		client: indexerv1.NewIndexerServiceClient(conn),
		logger: logger.With("component", "indexer-client"),
	}, nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Search executes an index query and returns ordered document references.
// Errors from the indexer (no matching index, index rebuilding, etc.) are
// translated from gRPC status codes to corresponding manager errors.
func (c *Client) Search(ctx context.Context, database string, plan manager.Plan) ([]manager.DocRef, error) {
	req, err := c.planToRequest(database, plan)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Search(ctx, req)
	if err != nil {
		return nil, c.translateError(err)
	}

	docs := make([]manager.DocRef, len(resp.Docs))
	for i, d := range resp.Docs {
		orderKey, err := base64.StdEncoding.DecodeString(d.OrderKey)
		if err != nil {
			return nil, fmt.Errorf("failed to decode order key: %w", err)
		}
		docs[i] = manager.DocRef{
			ID:       d.Id,
			OrderKey: orderKey,
		}
	}

	return docs, nil
}

// translateError converts gRPC status errors to manager errors.
func (c *Client) translateError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}

	switch st.Code() {
	case codes.NotFound:
		return manager.ErrNoMatchingIndex
	case codes.Unavailable:
		// Check message to distinguish between rebuilding and not ready
		if st.Message() == "index is rebuilding" {
			return manager.ErrIndexRebuilding
		}
		return manager.ErrIndexNotReady
	case codes.InvalidArgument:
		return manager.ErrInvalidPlan
	default:
		return err
	}
}

// Health returns the current health status of the indexer.
func (c *Client) Health(ctx context.Context) (manager.Health, error) {
	resp, err := c.client.Health(ctx, &indexerv1.HealthRequest{})
	if err != nil {
		return manager.Health{}, fmt.Errorf("health check failed: %w", err)
	}

	indexes := make(map[string]manager.IndexHealth)
	for k, v := range resp.Indexes {
		indexes[k] = manager.IndexHealth{
			State:    v.State,
			DocCount: v.DocCount,
		}
	}

	return manager.Health{
		Status:  manager.HealthStatus(resp.Status),
		Indexes: indexes,
	}, nil
}

// Stats returns aggregate statistics for the remote service instance.
func (c *Client) Stats(ctx context.Context) (manager.Stats, error) {
	resp, err := c.client.Stats(ctx, &indexerv1.StatsRequest{})
	if err != nil {
		return manager.Stats{}, fmt.Errorf("indexer stats at %q: %w", c.conn.Target(), err)
	}

	templateCount := int(resp.TemplateCount)
	if resp.TemplateCount < 0 || int64(templateCount) != resp.TemplateCount {
		return manager.Stats{}, fmt.Errorf("indexer stats at %q: invalid template count %d", c.conn.Target(), resp.TemplateCount)
	}
	return manager.Stats{
		TemplateCount: templateCount,
		EventsApplied: resp.EventsApplied,
		LastEventTime: resp.LastEventTime,
	}, nil
}

// IndexerState contains the complete indexer state.
type IndexerState struct {
	Desired    []IndexSpec
	Actual     []IndexInfo
	PendingOps []PendingOperation
}

// IndexSpec describes a desired index from configuration.
type IndexSpec struct {
	Pattern    string
	TemplateID string
	Fields     []IndexField
}

// IndexField describes a field in an index.
type IndexField struct {
	Field     string
	Direction string
}

// IndexInfo describes an actual index in memory.
type IndexInfo struct {
	Database   string
	Pattern    string
	TemplateID string
	State      string
	DocCount   int64
}

// PendingOperation describes a reconciler operation.
type PendingOperation struct {
	OpType     string
	Database   string
	Pattern    string
	TemplateID string
	Status     string
	Progress   int32
	Error      string
	StartedAt  int64
}

// GetState returns the complete index state.
func (c *Client) GetState(ctx context.Context, database, pattern string) (*IndexerState, error) {
	resp, err := c.client.GetState(ctx, &indexerv1.GetStateRequest{
		Database: database,
		Pattern:  pattern,
	})
	if err != nil {
		return nil, fmt.Errorf("get state failed: %w", err)
	}

	state := &IndexerState{
		Desired:    make([]IndexSpec, len(resp.Desired)),
		Actual:     make([]IndexInfo, len(resp.Actual)),
		PendingOps: make([]PendingOperation, len(resp.PendingOps)),
	}

	for i, d := range resp.Desired {
		fields := make([]IndexField, len(d.Fields))
		for j, f := range d.Fields {
			fields[j] = IndexField{
				Field:     f.Field,
				Direction: f.Direction,
			}
		}
		state.Desired[i] = IndexSpec{
			Pattern:    d.Pattern,
			TemplateID: d.TemplateId,
			Fields:     fields,
		}
	}

	for i, a := range resp.Actual {
		state.Actual[i] = IndexInfo{
			Database:   a.Database,
			Pattern:    a.Pattern,
			TemplateID: a.TemplateId,
			State:      a.State,
			DocCount:   a.DocCount,
		}
	}

	for i, p := range resp.PendingOps {
		state.PendingOps[i] = PendingOperation{
			OpType:     p.OpType,
			Database:   p.Database,
			Pattern:    p.Pattern,
			TemplateID: p.TemplateId,
			Status:     p.Status,
			Progress:   p.Progress,
			Error:      p.Error,
			StartedAt:  p.StartedAt,
		}
	}

	return state, nil
}

// ReloadResult contains the result of a reload operation.
type ReloadResult struct {
	TemplatesLoaded int32
	Errors          []string
}

// Reload reloads index templates from the configuration file.
func (c *Client) Reload(ctx context.Context) (*ReloadResult, error) {
	resp, err := c.client.Reload(ctx, &indexerv1.ReloadRequest{})
	if err != nil {
		return nil, fmt.Errorf("reload failed: %w", err)
	}

	return &ReloadResult{
		TemplatesLoaded: resp.TemplatesLoaded,
		Errors:          resp.Errors,
	}, nil
}

// InvalidateIndex marks index(es) for rebuild.
func (c *Client) InvalidateIndex(ctx context.Context, database, pattern, templateID string) (int32, error) {
	resp, err := c.client.InvalidateIndex(ctx, &indexerv1.InvalidateIndexRequest{
		Database:   database,
		Pattern:    pattern,
		TemplateId: templateID,
	})
	if err != nil {
		return 0, fmt.Errorf("invalidate index failed: %w", err)
	}

	return resp.IndexesInvalidated, nil
}

// planToRequest converts a manager.Plan to a gRPC SearchRequest.
func (c *Client) planToRequest(database string, plan manager.Plan) (*indexerv1.SearchRequest, error) {
	req := &indexerv1.SearchRequest{
		Database:   database,
		Collection: plan.Collection,
		Limit:      int32(plan.Limit),
		StartAfter: plan.StartAfter,
	}

	// Convert filters
	for _, f := range plan.Filters {
		valueBytes, err := json.Marshal(f.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to encode filter value: %w", err)
		}

		req.Filters = append(req.Filters, &indexerv1.Filter{
			Field: f.Field,
			Op:    string(f.Op),
			Value: valueBytes,
		})
	}

	// Convert order by
	for _, ob := range plan.OrderBy {
		dir := "asc"
		if ob.Direction == encoding.Desc {
			dir = "desc"
		}
		req.OrderBy = append(req.OrderBy, &indexerv1.OrderByField{
			Field:     ob.Field,
			Direction: dir,
		})
	}

	return req, nil
}
