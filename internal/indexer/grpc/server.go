// Package grpc provides the gRPC server adapter for the Indexer service.
package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	indexerv1 "github.com/syntrixbase/syntrix/api/gen/indexer/v1"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/indexer/template"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// LocalService is the interface that the gRPC server delegates to.
type LocalService interface {
	Search(ctx context.Context, database string, plan manager.Plan) ([]manager.DocRef, error)
	Health(ctx context.Context) (manager.Health, error)
	Stats(ctx context.Context) (manager.Stats, error)
	Manager() *manager.Manager
}

// Server implements the gRPC IndexerServiceServer interface.
type Server struct {
	indexerv1.UnimplementedIndexerServiceServer
	svc LocalService
}

// NewServer creates a new gRPC server adapter.
func NewServer(svc LocalService) *Server {
	return &Server{svc: svc}
}

// Search executes an index query and returns ordered document references.
func (s *Server) Search(ctx context.Context, req *indexerv1.SearchRequest) (*indexerv1.SearchResponse, error) {
	// Convert request to Plan
	plan, err := s.requestToPlan(req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}

	// Execute search
	docs, err := s.svc.Search(ctx, req.Database, plan)
	if err != nil {
		return nil, s.convertError(err)
	}

	// Convert results
	protoRefs := make([]*indexerv1.DocRef, len(docs))
	for i, doc := range docs {
		protoRefs[i] = &indexerv1.DocRef{
			Id:       doc.ID,
			OrderKey: base64.StdEncoding.EncodeToString(doc.OrderKey),
		}
	}

	return &indexerv1.SearchResponse{
		Docs: protoRefs,
	}, nil
}

// Health returns the current health status of the indexer.
func (s *Server) Health(ctx context.Context, req *indexerv1.HealthRequest) (*indexerv1.HealthResponse, error) {
	health, err := s.svc.Health(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "health check failed: %v", err)
	}

	indexes := make(map[string]*indexerv1.IndexHealth)
	for k, v := range health.Indexes {
		indexes[k] = &indexerv1.IndexHealth{
			State:    v.State,
			DocCount: v.DocCount,
		}
	}

	return &indexerv1.HealthResponse{
		Status:  string(health.Status),
		Indexes: indexes,
	}, nil
}

// Stats returns the service's aggregate statistics without scanning index data.
func (s *Server) Stats(ctx context.Context, req *indexerv1.StatsRequest) (*indexerv1.StatsResponse, error) {
	stats, err := s.svc.Stats(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, status.FromContextError(err).Err()
		}
		if st, ok := status.FromError(err); ok {
			return nil, st.Err()
		}
		return nil, status.Errorf(codes.Internal, "stats collection failed: %v", err)
	}

	return &indexerv1.StatsResponse{
		TemplateCount: int64(stats.TemplateCount),
		EventsApplied: stats.EventsApplied,
		LastEventTime: stats.LastEventTime,
	}, nil
}

// GetState returns the complete index state including desired, actual, and pending operations.
func (s *Server) GetState(ctx context.Context, req *indexerv1.GetStateRequest) (*indexerv1.IndexerState, error) {
	if err := ctx.Err(); err != nil {
		return nil, s.adminError(err)
	}
	mgr := s.svc.Manager()
	if mgr == nil {
		return nil, status.Error(codes.Internal, "manager not available")
	}

	// Build desired state from templates
	desired := s.buildDesiredState(mgr, req.Database, req.Pattern)

	// Build actual state from indexes
	actual, err := s.buildActualState(ctx, mgr, req.Database, req.Pattern)
	if err != nil {
		return nil, s.adminError(err)
	}

	// TODO: Get pending operations from reconciler when implemented
	pendingOps := []*indexerv1.PendingOperation{}

	return &indexerv1.IndexerState{
		Desired:    desired,
		Actual:     actual,
		PendingOps: pendingOps,
	}, nil
}

// Reload reloads index templates from the configuration file.
func (s *Server) Reload(ctx context.Context, req *indexerv1.ReloadRequest) (*indexerv1.ReloadResponse, error) {
	mgr := s.svc.Manager()
	if mgr == nil {
		return nil, status.Error(codes.Internal, "manager not available")
	}

	// TODO: Get template path from configuration
	// For now, return current template count without reloading
	templates := mgr.Templates()

	return &indexerv1.ReloadResponse{
		TemplatesLoaded: int32(len(templates)),
		Errors:          nil,
	}, nil
}

// InvalidateIndex fences matching active concrete generations until maintenance
// bootstrap. Broad requests cover the finite inventory; a concrete collection
// also resolves an empty generation certified by the bootstrap catalog.
func (s *Server) InvalidateIndex(ctx context.Context, req *indexerv1.InvalidateIndexRequest) (*indexerv1.InvalidateIndexResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, s.adminError(err)
	}
	mgr := s.svc.Manager()
	if mgr == nil {
		return nil, status.Error(codes.Internal, "manager not available")
	}
	if req.Database == "" {
		return nil, status.Error(codes.InvalidArgument, "database is required")
	}
	refs, err := mgr.Store().ListQueryIndexes()
	if err != nil {
		return nil, s.adminError(err)
	}
	if req.Pattern != "" && !strings.ContainsAny(req.Pattern, "*{}") {
		for _, match := range template.MatchTemplates(req.Pattern, adminTemplates(mgr, req.Database)) {
			refs = append(refs, store.QueryIndexRef{Database: req.Database, Collection: req.Pattern, TemplateFingerprint: match.Template.Fingerprint()})
		}
	}
	count := 0
	seen := make(map[store.QueryIndexRef]bool)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, s.adminError(err)
		}
		if ref.Database != req.Database {
			continue
		}
		tmpl := findAdminTemplate(mgr, ref.Database, ref.TemplateFingerprint)
		if !matchesAdminPattern(req.Pattern, ref, tmpl) {
			continue
		}
		if req.TemplateId != "" && (tmpl == nil || tmpl.Identity() != req.TemplateId) {
			continue
		}
		generation, found, err := mgr.Store().ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
		if err != nil {
			return nil, s.adminError(err)
		}
		if !found || (ref.Generation != "" && ref.Generation != generation.ID) {
			continue
		}
		ref.Generation = generation.ID
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if err := mgr.Store().SetFailure(ref, "index invalidated; maintenance bootstrap required"); err != nil {
			return nil, s.adminError(err)
		}
		count++
	}
	return &indexerv1.InvalidateIndexResponse{IndexesInvalidated: int32(count)}, nil
}

func (s *Server) adminError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	return status.Errorf(codes.Internal, "index administration failed: %v", err)
}

func adminTemplates(mgr *manager.Manager, database string) []template.Template {
	if len(mgr.DatabaseTemplates()) > 0 {
		return mgr.TemplatesForDatabase(database)
	}
	return mgr.Templates()
}

func findAdminTemplate(mgr *manager.Manager, database, fingerprint string) *template.Template {
	templates := adminTemplates(mgr, database)
	for i := range templates {
		if templates[i].Fingerprint() == fingerprint {
			return &templates[i]
		}
	}
	return nil
}

func matchesAdminPattern(pattern string, ref store.QueryIndexRef, tmpl *template.Template) bool {
	return pattern == "" || pattern == ref.Collection || (tmpl != nil && template.NormalizePattern(pattern) == tmpl.NormalizedPattern())
}

// requestToPlan converts a gRPC SearchRequest to a manager.Plan.
func (s *Server) requestToPlan(req *indexerv1.SearchRequest) (manager.Plan, error) {
	plan := manager.Plan{
		Collection: req.Collection,
		Limit:      int(req.Limit),
		StartAfter: req.StartAfter,
	}

	// Convert filters
	for _, f := range req.Filters {
		op, err := parseFilterOp(f.Op)
		if err != nil {
			return plan, err
		}

		// Decode JSON value
		var value any
		if len(f.Value) > 0 {
			if err := json.Unmarshal(f.Value, &value); err != nil {
				return plan, err
			}
		}

		plan.Filters = append(plan.Filters, manager.Filter{
			Field: f.Field,
			Op:    op,
			Value: value,
		})
	}

	// Convert order by
	for _, ob := range req.OrderBy {
		dir := encoding.Asc
		if ob.Direction == "desc" {
			dir = encoding.Desc
		}
		plan.OrderBy = append(plan.OrderBy, manager.OrderField{
			Field:     ob.Field,
			Direction: dir,
		})
	}

	return plan, nil
}

// parseFilterOp parses a filter operator string to FilterOp.
func parseFilterOp(op string) (manager.FilterOp, error) {
	switch op {
	case "eq":
		return manager.FilterEq, nil
	case "gt":
		return manager.FilterGt, nil
	case "lt":
		return manager.FilterLt, nil
	case "gte":
		return manager.FilterGte, nil
	case "lte":
		return manager.FilterLte, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "invalid filter operator: %s", op)
	}
}

// convertError converts internal errors to gRPC status errors.
func (s *Server) convertError(err error) error {
	switch err {
	case manager.ErrNoMatchingIndex:
		return status.Error(codes.NotFound, "no matching index for query")
	case manager.ErrIndexNotReady:
		return status.Error(codes.Unavailable, "index not ready")
	case manager.ErrIndexRebuilding:
		return status.Error(codes.Unavailable, "index is rebuilding")
	case manager.ErrInvalidPlan:
		return status.Error(codes.InvalidArgument, "invalid query plan")
	default:
		return status.Errorf(codes.Internal, "internal error: %v", err)
	}
}

// buildDesiredState builds the desired index specs from templates.
func (s *Server) buildDesiredState(mgr *manager.Manager, filterDB, filterPattern string) []*indexerv1.IndexSpec {
	templates := mgr.Templates()
	if filterDB != "" {
		templates = adminTemplates(mgr, filterDB)
	}
	specs := make([]*indexerv1.IndexSpec, 0, len(templates))

	for _, tmpl := range templates {
		// Apply pattern filter
		if filterPattern != "" && tmpl.NormalizedPattern() != template.NormalizePattern(filterPattern) && len(template.MatchTemplates(filterPattern, []template.Template{tmpl})) == 0 {
			continue
		}

		fields := make([]*indexerv1.IndexField, len(tmpl.Fields))
		for i, f := range tmpl.Fields {
			dir := "asc"
			if f.Order == template.Desc {
				dir = "desc"
			}
			fields[i] = &indexerv1.IndexField{
				Field:     f.Field,
				Direction: dir,
			}
		}

		specs = append(specs, &indexerv1.IndexSpec{
			Pattern:    tmpl.NormalizedPattern(),
			TemplateId: tmpl.Identity(),
			Fields:     fields,
		})
	}

	return specs
}

// buildActualState reports the active generation for each concrete partition.
// DocCount=-1 means unavailable: administration never scans postings to count
// documents, and the generation inventory does not maintain this aggregate.
func (s *Server) buildActualState(ctx context.Context, mgr *manager.Manager, filterDB, filterPattern string) ([]*indexerv1.IndexInfo, error) {
	refs, err := mgr.Store().ListQueryIndexes()
	if err != nil {
		return nil, err
	}
	var infos []*indexerv1.IndexInfo
	seen := make(map[store.QueryIndexRef]bool)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if filterDB != "" && filterDB != ref.Database {
			continue
		}
		tmpl := findAdminTemplate(mgr, ref.Database, ref.TemplateFingerprint)
		if !matchesAdminPattern(filterPattern, ref, tmpl) {
			continue
		}
		generation, found, err := mgr.Store().ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
		if err != nil {
			return nil, err
		}
		if !found || generation.ID != ref.Generation || seen[ref] {
			continue
		}
		seen[ref] = true
		state := store.IndexStateRebuilding
		if generation.Failure != "" {
			state = store.IndexStateFailed
		} else if generation.Ready {
			state = store.IndexStateHealthy
		}
		identity := ref.TemplateFingerprint
		if tmpl != nil {
			identity = tmpl.Identity()
		}
		infos = append(infos, &indexerv1.IndexInfo{Database: ref.Database, Pattern: ref.Collection, TemplateId: identity, State: string(state), DocCount: -1})
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Database != infos[j].Database {
			return infos[i].Database < infos[j].Database
		}
		if infos[i].Pattern != infos[j].Pattern {
			return infos[i].Pattern < infos[j].Pattern
		}
		return infos[i].TemplateId < infos[j].TemplateId
	})
	return infos, nil
}
