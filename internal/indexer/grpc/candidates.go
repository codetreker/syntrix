package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	indexerv1 "github.com/syntrixbase/syntrix/api/gen/indexer/v1"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const candidateMessageLimit = 1 << 20

type candidateService interface {
	OpenCandidates(context.Context, string, manager.Plan) (manager.CandidateStream, error)
}

func (s *Server) OpenCandidates(req *indexerv1.CandidateRequest, out indexerv1.IndexerService_OpenCandidatesServer) (resultErr error) {
	ctx := ctxkeys.IncomingRequestContext(out.Context())
	started := time.Now()
	logger := slog.Default().With("component", "indexer")
	requestID := ctxkeys.RequestID(ctx)
	var stream manager.CandidateStream
	var admitted manager.CandidateMetadata
	groups := 0
	defer func() {
		examined := int64(0)
		if stream != nil {
			examined = stream.Examined()
		}
		logger.DebugContext(ctx, "Index candidates completed",
			"request_id", requestID, "plan_id", admitted.BranchHash,
			"template_fingerprint", admitted.TemplateFingerprint, "generation_id", candidateGenerationID(admitted.Generation),
			"branch_count", len(admitted.Branches), "candidates", groups, "examined", examined,
			"duration_ms", time.Since(started).Milliseconds(), "reason", candidateCompletionReason(resultErr))
	}()
	if proto.Size(req) > candidateMessageLimit {
		return candidateError(fmt.Errorf("%w: candidate request exceeds 1 MiB", manager.ErrInvalidPlan))
	}
	plan, err := candidateRequestToPlan(req)
	if err != nil {
		return candidateError(fmt.Errorf("%w: %v", manager.ErrInvalidPlan, err))
	}
	svc, ok := s.svc.(candidateService)
	if !ok {
		return candidateError(manager.ErrIndexNotReady)
	}
	stream, err = svc.OpenCandidates(ctx, req.Database, plan)
	if err != nil {
		return candidateError(err)
	}
	defer stream.Close()
	admitted = stream.Metadata()
	logger.DebugContext(ctx, "Index candidates admitted", "request_id", requestID,
		"plan_id", admitted.BranchHash, "template_fingerprint", admitted.TemplateFingerprint,
		"generation_id", candidateGenerationID(admitted.Generation), "branch_count", len(admitted.Branches))
	if err := sendCandidate(out, &indexerv1.CandidateResponse{
		Payload: &indexerv1.CandidateResponse_Metadata{Metadata: candidateMetadataToProto(stream.Metadata())},
	}); err != nil {
		return err
	}
	for {
		group, ok, err := stream.Next()
		if err != nil {
			return candidateError(err)
		}
		response := &indexerv1.CandidateResponse{Examined: stream.Examined()}
		if !ok {
			response.Payload = &indexerv1.CandidateResponse_Complete{Complete: &indexerv1.CandidateComplete{}}
			if err := stream.Close(); err != nil {
				return candidateError(err)
			}
			return sendCandidate(out, response)
		}
		branches := make([]int32, len(group.Branches))
		for i, branch := range group.Branches {
			branches[i] = int32(branch)
		}
		response.Payload = &indexerv1.CandidateResponse_Group{Group: &indexerv1.CandidateGroup{
			Id: group.ID, Position: group.Position, Branches: branches,
		}}
		if err := sendCandidate(out, response); err != nil {
			return err
		}
		groups++
	}
}

func candidateGenerationID(generation string) string {
	if generation == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(generation))
	return hex.EncodeToString(sum[:])
}

func candidateCompletionReason(err error) string {
	if err == nil {
		return "exhausted"
	}
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Domain == "syntrix.indexer" {
			switch info.Reason {
			case "STALE_CURSOR":
				return "stale_cursor"
			case "WORK_LIMIT":
				return "work_limit"
			case "NO_MATCHING_INDEX":
				return "no_complete_index_plan"
			case "INDEX_NOT_READY", "INDEX_REBUILDING":
				return "index_unavailable"
			case "INVALID_PLAN":
				return "invalid_query"
			}
		}
	}
	switch status.Code(err) {
	case codes.Canceled:
		return "canceled"
	case codes.DeadlineExceeded:
		return "deadline_exceeded"
	}
	return "internal_error"
}

func sendCandidate(out indexerv1.IndexerService_OpenCandidatesServer, response *indexerv1.CandidateResponse) error {
	if proto.Size(response) > candidateMessageLimit {
		return candidateError(fmt.Errorf("%w: candidate message exceeds 1 MiB", store.ErrWorkLimit))
	}
	return out.Send(response)
}

func candidateRequestToPlan(req *indexerv1.CandidateRequest) (manager.Plan, error) {
	plan := manager.Plan{
		Collection: req.Collection, ShowDeleted: req.ShowDeleted, AfterPosition: req.AfterPosition,
		TemplateFingerprint: req.TemplateFingerprint, Generation: req.Generation,
		BranchHash: req.BranchHash, MaxExamined: req.MaxExamined,
	}
	for _, filter := range req.Filters {
		if filter == nil {
			return plan, fmt.Errorf("filter is missing")
		}
		value, err := model.DecodeTypedValue(filter.TypedValue)
		if err != nil {
			return plan, fmt.Errorf("filter %q: %w", filter.Field, err)
		}
		plan.Filters = append(plan.Filters, manager.Filter{Field: filter.Field, Op: manager.FilterOp(filter.Op), Value: value})
	}
	for _, field := range req.OrderBy {
		if field == nil || (field.Direction != "asc" && field.Direction != "desc") {
			return plan, fmt.Errorf("order direction must be asc or desc")
		}
		direction := encoding.Asc
		if field.Direction == "desc" {
			direction = encoding.Desc
		}
		plan.OrderBy = append(plan.OrderBy, manager.OrderField{Field: field.Field, Direction: direction})
	}
	return plan, nil
}

func candidateMetadataToProto(meta manager.CandidateMetadata) *indexerv1.CandidateMetadata {
	out := &indexerv1.CandidateMetadata{
		TemplateName: meta.Template.Name, CollectionPattern: meta.Template.CollectionPattern,
		IncludeDeleted: meta.Template.IncludeDeleted, TemplateFingerprint: meta.TemplateFingerprint,
		Generation: meta.Generation, BranchHash: meta.BranchHash,
	}
	for _, field := range meta.Template.Fields {
		out.Fields = append(out.Fields, &indexerv1.CandidateTemplateField{Field: field.Field, Direction: string(field.Order), Mode: string(field.Mode)})
	}
	for _, field := range meta.EffectiveOrder {
		direction := "asc"
		if field.Direction == encoding.Desc {
			direction = "desc"
		}
		out.EffectiveOrder = append(out.EffectiveOrder, &indexerv1.OrderByField{Field: field.Field, Direction: direction})
	}
	for _, branch := range meta.Branches {
		out.Branches = append(out.Branches, &indexerv1.CandidateBranch{Prefix: branch.Prefix, FixedFields: int32(branch.FixedFields), Lower: branch.Lower, Upper: branch.Upper})
	}
	for _, assignment := range meta.Assignments {
		out.Assignments = append(out.Assignments, &indexerv1.PredicateAssignment{Predicate: int32(assignment.Predicate), Access: assignment.Access, Residual: assignment.Residual})
	}
	return out
}

func candidateError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	code, reason := codes.Internal, "INTERNAL"
	switch {
	case errors.Is(err, manager.ErrStaleCursor):
		code, reason = codes.FailedPrecondition, "STALE_CURSOR"
	case errors.Is(err, store.ErrWorkLimit):
		code, reason = codes.ResourceExhausted, "WORK_LIMIT"
	case errors.Is(err, manager.ErrNoMatchingIndex):
		code, reason = codes.NotFound, "NO_MATCHING_INDEX"
	case errors.Is(err, manager.ErrIndexNotReady):
		code, reason = codes.Unavailable, "INDEX_NOT_READY"
	case errors.Is(err, manager.ErrIndexRebuilding):
		code, reason = codes.Unavailable, "INDEX_REBUILDING"
	case errors.Is(err, manager.ErrInvalidPlan), errors.Is(err, model.ErrInvalidQuery):
		code, reason = codes.InvalidArgument, "INVALID_PLAN"
	}
	st, detailErr := status.New(code, err.Error()).WithDetails(&errdetails.ErrorInfo{Domain: "syntrix.indexer", Reason: reason})
	if detailErr != nil {
		return status.Error(codes.Internal, detailErr.Error())
	}
	return st.Err()
}
