package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	indexerv1 "github.com/syntrixbase/syntrix/api/gen/indexer/v1"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/indexer/template"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const candidateMessageLimit = 1 << 20

// OpenCandidates waits for metadata before returning ownership of the stream.
// Callers must Close the stream after consuming the candidates they need.
func (c *Client) OpenCandidates(ctx context.Context, database string, plan manager.Plan) (manager.CandidateStream, error) {
	req, err := candidatePlanToRequest(database, plan)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctxkeys.OutgoingRequestContext(ctx))
	rpc, err := c.client.OpenCandidates(ctx, req, grpc.MaxCallRecvMsgSize(candidateMessageLimit), grpc.MaxCallSendMsgSize(candidateMessageLimit))
	if err != nil {
		cancel()
		return nil, translateCandidateError(err)
	}
	first, err := rpc.Recv()
	if err != nil {
		cancel()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("candidate stream ended before metadata: %w", io.ErrUnexpectedEOF)
		}
		return nil, translateCandidateError(err)
	}
	if first.GetMetadata() == nil || first.Examined != 0 {
		cancel()
		return nil, fmt.Errorf("candidate stream must begin with unexamined metadata")
	}
	metadata, err := candidateMetadataFromProto(first.GetMetadata())
	if err != nil {
		cancel()
		return nil, err
	}
	return &candidateStream{rpc: rpc, cancel: cancel, metadata: metadata}, nil
}

type candidateStream struct {
	rpc      indexerv1.IndexerService_OpenCandidatesClient
	cancel   context.CancelFunc
	metadata manager.CandidateMetadata
	examined int64
	closed   atomic.Bool
}

func (s *candidateStream) Metadata() manager.CandidateMetadata { return s.metadata }
func (s *candidateStream) Examined() int64                     { return s.examined }
func (s *candidateStream) Close() error {
	s.closed.Store(true)
	s.cancel()
	return nil
}

func (s *candidateStream) Next() (manager.CandidateGroup, bool, error) {
	if s.closed.Load() {
		return manager.CandidateGroup{}, false, nil
	}
	response, err := s.rpc.Recv()
	if err != nil {
		s.Close()
		if errors.Is(err, io.EOF) {
			return manager.CandidateGroup{}, false, fmt.Errorf("candidate stream ended without completion: %w", io.ErrUnexpectedEOF)
		}
		return manager.CandidateGroup{}, false, translateCandidateError(err)
	}
	if response.Examined < s.examined {
		s.Close()
		return manager.CandidateGroup{}, false, fmt.Errorf("candidate examined count regressed")
	}
	s.examined = response.Examined
	if response.GetComplete() != nil {
		_, err := s.rpc.Recv()
		s.Close()
		if !errors.Is(err, io.EOF) {
			if err != nil {
				return manager.CandidateGroup{}, false, translateCandidateError(err)
			}
			return manager.CandidateGroup{}, false, fmt.Errorf("candidate payload follows completion")
		}
		return manager.CandidateGroup{}, false, nil
	}
	group := response.GetGroup()
	if group == nil || group.Id == "" || len(group.Position) == 0 || len(group.Position) > manager.MaxCandidatePositionBytes || len(group.Branches) == 0 || len(group.Branches) > manager.MaxCandidateBranches {
		s.Close()
		return manager.CandidateGroup{}, false, fmt.Errorf("invalid candidate group")
	}
	branches := make([]int, len(group.Branches))
	seen := make(map[int32]bool, len(group.Branches))
	for i, branch := range group.Branches {
		if branch < 0 || int(branch) >= len(s.metadata.Branches) || seen[branch] {
			s.Close()
			return manager.CandidateGroup{}, false, fmt.Errorf("invalid candidate branch identifier %d", branch)
		}
		seen[branch] = true
		branches[i] = int(branch)
	}
	return manager.CandidateGroup{ID: group.Id, Position: group.Position, Branches: branches}, true, nil
}

func candidatePlanToRequest(database string, plan manager.Plan) (*indexerv1.CandidateRequest, error) {
	req := &indexerv1.CandidateRequest{
		Database: database, Collection: plan.Collection, ShowDeleted: plan.ShowDeleted,
		AfterPosition: plan.AfterPosition, TemplateFingerprint: plan.TemplateFingerprint,
		Generation: plan.Generation, BranchHash: plan.BranchHash, MaxExamined: plan.MaxExamined,
	}
	for _, filter := range plan.Filters {
		value, err := model.EncodeTypedValue(filter.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: filter %q: %v", manager.ErrInvalidPlan, filter.Field, err)
		}
		req.Filters = append(req.Filters, &indexerv1.CandidateFilter{Field: filter.Field, Op: string(filter.Op), TypedValue: value})
	}
	for _, field := range plan.OrderBy {
		if field.Direction != encoding.Asc && field.Direction != encoding.Desc {
			return nil, fmt.Errorf("%w: invalid ordering direction", manager.ErrInvalidPlan)
		}
		direction := "asc"
		if field.Direction == encoding.Desc {
			direction = "desc"
		}
		req.OrderBy = append(req.OrderBy, &indexerv1.OrderByField{Field: field.Field, Direction: direction})
	}
	if proto.Size(req) > candidateMessageLimit {
		return nil, fmt.Errorf("%w: candidate request exceeds 1 MiB", manager.ErrInvalidPlan)
	}
	return req, nil
}

func candidateMetadataFromProto(in *indexerv1.CandidateMetadata) (manager.CandidateMetadata, error) {
	meta := manager.CandidateMetadata{
		Template:            template.Template{Name: in.TemplateName, CollectionPattern: in.CollectionPattern, IncludeDeleted: in.IncludeDeleted},
		TemplateFingerprint: in.TemplateFingerprint, Generation: in.Generation, BranchHash: in.BranchHash,
	}
	if len(in.Branches) > manager.MaxCandidateBranches {
		return meta, fmt.Errorf("candidate metadata exceeds branch limit")
	}
	for _, field := range in.Fields {
		if field == nil {
			return meta, fmt.Errorf("candidate metadata field is missing")
		}
		meta.Template.Fields = append(meta.Template.Fields, template.Field{Field: field.Field, Order: template.Direction(field.Direction), Mode: template.Mode(field.Mode)})
	}
	for _, field := range in.EffectiveOrder {
		if field == nil || (field.Direction != "asc" && field.Direction != "desc") {
			return meta, fmt.Errorf("invalid candidate ordering")
		}
		direction := encoding.Asc
		if field.Direction == "desc" {
			direction = encoding.Desc
		}
		meta.EffectiveOrder = append(meta.EffectiveOrder, manager.OrderField{Field: field.Field, Direction: direction})
	}
	for _, branch := range in.Branches {
		if branch == nil || branch.FixedFields < 0 || int(branch.FixedFields) > len(in.Fields) {
			return meta, fmt.Errorf("invalid candidate branch metadata")
		}
		meta.Branches = append(meta.Branches, manager.CandidateBranch{Prefix: branch.Prefix, FixedFields: int(branch.FixedFields), Lower: branch.Lower, Upper: branch.Upper})
	}
	for _, assignment := range in.Assignments {
		if assignment == nil || assignment.Predicate < 0 || (!assignment.Access && !assignment.Residual) {
			return meta, fmt.Errorf("invalid candidate predicate assignment")
		}
		meta.Assignments = append(meta.Assignments, manager.PredicateAssignment{Predicate: int(assignment.Predicate), Access: assignment.Access, Residual: assignment.Residual})
	}
	return meta, nil
}

func translateCandidateError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	if st.Code() == codes.Canceled {
		return fmt.Errorf("%w: %w", context.Canceled, err)
	}
	if st.Code() == codes.DeadlineExceeded {
		return fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
	}
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok || info.Domain != "syntrix.indexer" {
			continue
		}
		var cause error
		switch info.Reason {
		case "NO_MATCHING_INDEX":
			cause = manager.ErrNoMatchingIndex
		case "INDEX_NOT_READY":
			cause = manager.ErrIndexNotReady
		case "INDEX_REBUILDING":
			cause = manager.ErrIndexRebuilding
		case "INVALID_PLAN":
			cause = manager.ErrInvalidPlan
		case "STALE_CURSOR":
			cause = manager.ErrStaleCursor
		case "WORK_LIMIT":
			cause = store.ErrWorkLimit
		}
		if cause != nil {
			return fmt.Errorf("%w: %w", cause, err)
		}
	}
	return err
}
