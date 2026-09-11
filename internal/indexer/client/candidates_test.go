package client

import (
	"context"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	indexerv1 "github.com/syntrixbase/syntrix/api/gen/indexer/v1"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/indexer/template"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func candidateMetadataMessage() *indexerv1.CandidateResponse {
	return &indexerv1.CandidateResponse{Payload: &indexerv1.CandidateResponse_Metadata{Metadata: &indexerv1.CandidateMetadata{
		Branches: []*indexerv1.CandidateBranch{{}},
	}}}
}

func TestCandidateStreamRejectsMissingCompletion(t *testing.T) {
	client, cleanup := setupTestServer(t, &mockServer{candidatesFn: func(_ *indexerv1.CandidateRequest, stream indexerv1.IndexerService_OpenCandidatesServer) error {
		return stream.Send(candidateMetadataMessage())
	}})
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.OpenCandidates(ctx, "db", manager.Plan{})
	require.NoError(t, err)
	_, more, err := stream.Next()
	assert.False(t, more)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestCandidateStreamCloseCancelsServer(t *testing.T) {
	canceled := make(chan struct{})
	client, cleanup := setupTestServer(t, &mockServer{candidatesFn: func(_ *indexerv1.CandidateRequest, stream indexerv1.IndexerService_OpenCandidatesServer) error {
		if err := stream.Send(candidateMetadataMessage()); err != nil {
			return err
		}
		<-stream.Context().Done()
		close(canceled)
		return stream.Context().Err()
	}})
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.OpenCandidates(ctx, "db", manager.Plan{})
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("server did not observe stream cancellation")
	}
}

func TestCandidateStreamRejectsMalformedMessages(t *testing.T) {
	for _, tc := range []struct {
		name      string
		messages  []*indexerv1.CandidateResponse
		openFails bool
	}{
		{"missing metadata", nil, true},
		{"group before metadata", []*indexerv1.CandidateResponse{{Payload: &indexerv1.CandidateResponse_Group{Group: &indexerv1.CandidateGroup{Id: "x"}}}}, true},
		{"metadata repeated", []*indexerv1.CandidateResponse{candidateMetadataMessage(), candidateMetadataMessage()}, false},
		{"negative count", []*indexerv1.CandidateResponse{candidateMetadataMessage(), {Examined: -1, Payload: &indexerv1.CandidateResponse_Complete{Complete: &indexerv1.CandidateComplete{}}}}, false},
		{"invalid branch", []*indexerv1.CandidateResponse{candidateMetadataMessage(), {Payload: &indexerv1.CandidateResponse_Group{Group: &indexerv1.CandidateGroup{Id: "x", Position: []byte{1}, Branches: []int32{1}}}}}, false},
		{"duplicate branch", []*indexerv1.CandidateResponse{candidateMetadataMessage(), {Payload: &indexerv1.CandidateResponse_Group{Group: &indexerv1.CandidateGroup{Id: "x", Position: []byte{1}, Branches: []int32{0, 0}}}}}, false},
		{"payload after completion", []*indexerv1.CandidateResponse{candidateMetadataMessage(), {Payload: &indexerv1.CandidateResponse_Complete{Complete: &indexerv1.CandidateComplete{}}}, candidateMetadataMessage()}, false},
		{"oversize message", []*indexerv1.CandidateResponse{candidateMetadataMessage(), {Payload: &indexerv1.CandidateResponse_Group{Group: &indexerv1.CandidateGroup{Id: "x", Position: make([]byte, candidateMessageLimit), Branches: []int32{0}}}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := setupTestServer(t, &mockServer{candidatesFn: func(_ *indexerv1.CandidateRequest, stream indexerv1.IndexerService_OpenCandidatesServer) error {
				for _, msg := range tc.messages {
					if err := stream.Send(msg); err != nil {
						return err
					}
				}
				return nil
			}})
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := client.OpenCandidates(ctx, "db", manager.Plan{})
			if tc.openFails {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			defer stream.Close()
			_, more, err := stream.Next()
			assert.False(t, more)
			require.Error(t, err)
		})
	}
}

func TestCandidateErrorsRequireTypedDetails(t *testing.T) {
	for _, tc := range []struct {
		reason string
		cause  error
	}{
		{"NO_MATCHING_INDEX", manager.ErrNoMatchingIndex},
		{"INDEX_NOT_READY", manager.ErrIndexNotReady},
		{"INDEX_REBUILDING", manager.ErrIndexRebuilding},
		{"INVALID_PLAN", manager.ErrInvalidPlan},
		{"STALE_CURSOR", manager.ErrStaleCursor},
		{"WORK_LIMIT", store.ErrWorkLimit},
	} {
		st, err := status.New(codes.Unknown, "wording can change").WithDetails(&errdetails.ErrorInfo{Domain: "syntrix.indexer", Reason: tc.reason})
		require.NoError(t, err)
		assert.ErrorIs(t, translateCandidateError(st.Err()), tc.cause)
	}
	plain := status.Error(codes.Unavailable, "index is rebuilding")
	assert.Same(t, plain, translateCandidateError(plain))
	wrongDomain, err := status.New(codes.Unavailable, "index is rebuilding").WithDetails(&errdetails.ErrorInfo{Domain: "other", Reason: "INDEX_REBUILDING"})
	require.NoError(t, err)
	assert.NotErrorIs(t, translateCandidateError(wrongDomain.Err()), manager.ErrIndexRebuilding)
}

func TestCandidateWireContract(t *testing.T) {
	requests := make(chan *indexerv1.CandidateRequest, 1)
	client, cleanup := setupTestServer(t, &mockServer{candidatesFn: func(req *indexerv1.CandidateRequest, stream indexerv1.IndexerService_OpenCandidatesServer) error {
		requests <- req
		messages := []*indexerv1.CandidateResponse{
			{Payload: &indexerv1.CandidateResponse_Metadata{Metadata: &indexerv1.CandidateMetadata{
				TemplateName: "membership-score", CollectionPattern: "items", IncludeDeleted: true,
				TemplateFingerprint: "fingerprint", Generation: "generation", BranchHash: "branches",
				Fields:         []*indexerv1.CandidateTemplateField{{Field: "tags", Mode: "membership", Direction: "asc"}, {Field: "score", Mode: "scalar", Direction: "desc"}},
				EffectiveOrder: []*indexerv1.OrderByField{{Field: "score", Direction: "desc"}, {Field: "id", Direction: "asc"}},
				Branches:       []*indexerv1.CandidateBranch{{Prefix: []byte{1, 2}, FixedFields: 1, Lower: []byte{1, 3}, Upper: []byte{1, 4}}},
				Assignments:    []*indexerv1.PredicateAssignment{{Predicate: 0, Access: true, Residual: true}, {Predicate: 1, Residual: true}},
			}}},
			{Examined: 2, Payload: &indexerv1.CandidateResponse_Group{Group: &indexerv1.CandidateGroup{Id: "item", Position: []byte{2, 3}, Branches: []int32{0}}}},
			{Examined: 3, Payload: &indexerv1.CandidateResponse_Complete{Complete: &indexerv1.CandidateComplete{}}},
		}
		for _, message := range messages {
			if err := stream.Send(message); err != nil {
				return err
			}
		}
		return nil
	}})
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	values := []any{int64(math.MinInt64), int64(math.MaxInt64), float64(1.5)}
	stream, err := client.OpenCandidates(ctx, "db", manager.Plan{
		Collection: "items", ShowDeleted: true, AfterPosition: []byte{1, 2}, TemplateFingerprint: "fingerprint", Generation: "generation", BranchHash: "branches", MaxExamined: 20,
		Filters: []manager.Filter{{Field: "tags", Op: manager.FilterIn, Value: values}},
		OrderBy: []manager.OrderField{{Field: "score", Direction: encoding.Desc}, {Field: "id", Direction: encoding.Asc}},
	})
	require.NoError(t, err)
	defer stream.Close()
	req := <-requests
	assert.Equal(t, "db", req.Database)
	assert.Equal(t, "items", req.Collection)
	assert.True(t, req.ShowDeleted)
	assert.Equal(t, []byte{1, 2}, req.AfterPosition)
	assert.Equal(t, "fingerprint", req.TemplateFingerprint)
	assert.Equal(t, "generation", req.Generation)
	assert.Equal(t, "branches", req.BranchHash)
	assert.Equal(t, int64(20), req.MaxExamined)
	require.Len(t, req.Filters, 1)
	decoded, err := model.DecodeTypedValue(req.Filters[0].TypedValue)
	require.NoError(t, err)
	assert.Equal(t, values, decoded)
	assert.Equal(t, "in", req.Filters[0].Op)
	assert.Equal(t, "tags", req.Filters[0].Field)
	require.Len(t, req.OrderBy, 2)
	assert.Equal(t, "desc", req.OrderBy[0].Direction)
	assert.Equal(t, "asc", req.OrderBy[1].Direction)
	meta := stream.Metadata()
	assert.Equal(t, "membership-score", meta.Template.Name)
	assert.True(t, meta.Template.IncludeDeleted)
	assert.Equal(t, template.Membership, meta.Template.Fields[0].Mode)
	assert.Equal(t, template.Desc, meta.Template.Fields[1].Order)
	assert.Equal(t, encoding.Desc, meta.EffectiveOrder[0].Direction)
	assert.Equal(t, encoding.Asc, meta.EffectiveOrder[1].Direction)
	assert.Equal(t, manager.CandidateBranch{Prefix: []byte{1, 2}, FixedFields: 1, Lower: []byte{1, 3}, Upper: []byte{1, 4}}, meta.Branches[0])
	assert.Equal(t, []manager.PredicateAssignment{{Predicate: 0, Access: true, Residual: true}, {Predicate: 1, Residual: true}}, meta.Assignments)
	assert.Zero(t, stream.Examined())
	group, more, err := stream.Next()
	require.NoError(t, err)
	require.True(t, more)
	assert.Equal(t, manager.CandidateGroup{ID: "item", Position: []byte{2, 3}, Branches: []int{0}}, group)
	assert.Equal(t, int64(2), stream.Examined())
	_, more, err = stream.Next()
	require.NoError(t, err)
	assert.False(t, more)
	assert.Equal(t, int64(3), stream.Examined())
	_, more, err = stream.Next()
	require.NoError(t, err)
	assert.False(t, more)
}

func TestCandidateRejectsInvalidPlansBeforeRPC(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan manager.Plan
	}{
		{"nonfinite operand", manager.Plan{Filters: []manager.Filter{{Field: "score", Value: math.Inf(1)}}}},
		{"invalid direction", manager.Plan{OrderBy: []manager.OrderField{{Field: "score", Direction: encoding.Direction(200)}}}},
		{"request limit", manager.Plan{Collection: strings.Repeat("x", candidateMessageLimit)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{}
			stream, err := client.OpenCandidates(context.Background(), "db", tc.plan)
			assert.Nil(t, stream)
			assert.ErrorIs(t, err, manager.ErrInvalidPlan)
		})
	}
}

func TestCandidateRejectsInvalidMetadataOverWire(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata *indexerv1.CandidateMetadata
	}{
		{"branch limit", &indexerv1.CandidateMetadata{Branches: make([]*indexerv1.CandidateBranch, manager.MaxCandidateBranches+1)}},
		{"ordering direction", &indexerv1.CandidateMetadata{EffectiveOrder: []*indexerv1.OrderByField{{Field: "score", Direction: "sideways"}}}},
		{"fixed prefix exceeds fields", &indexerv1.CandidateMetadata{Branches: []*indexerv1.CandidateBranch{{FixedFields: 1}}}},
		{"unassigned predicate", &indexerv1.CandidateMetadata{Assignments: []*indexerv1.PredicateAssignment{{Predicate: 0}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := setupTestServer(t, &mockServer{candidatesFn: func(_ *indexerv1.CandidateRequest, stream indexerv1.IndexerService_OpenCandidatesServer) error {
				return stream.Send(&indexerv1.CandidateResponse{Payload: &indexerv1.CandidateResponse_Metadata{Metadata: tc.metadata}})
			}})
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := client.OpenCandidates(ctx, "db", manager.Plan{})
			require.Error(t, err)
			assert.Nil(t, stream)
		})
	}
}

func TestCandidatePropagatesRPCFailures(t *testing.T) {
	for _, tc := range []struct {
		name           string
		sendCompletion bool
	}{
		{"before metadata", false}, {"after completion payload", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := status.New(codes.ResourceExhausted, "remote quota exhausted").WithDetails(&errdetails.ErrorInfo{Domain: "syntrix.indexer", Reason: "WORK_LIMIT"})
			require.NoError(t, err)
			client, cleanup := setupTestServer(t, &mockServer{candidatesFn: func(_ *indexerv1.CandidateRequest, stream indexerv1.IndexerService_OpenCandidatesServer) error {
				if tc.sendCompletion {
					if err := stream.Send(candidateMetadataMessage()); err != nil {
						return err
					}
					if err := stream.Send(&indexerv1.CandidateResponse{Payload: &indexerv1.CandidateResponse_Complete{Complete: &indexerv1.CandidateComplete{}}}); err != nil {
						return err
					}
				}
				return st.Err()
			}})
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := client.OpenCandidates(ctx, "db", manager.Plan{})
			if tc.sendCompletion {
				require.NoError(t, err)
				defer stream.Close()
				_, more, nextErr := stream.Next()
				assert.False(t, more)
				err = nextErr
			}
			assert.ErrorIs(t, err, store.ErrWorkLimit)
			assert.Equal(t, codes.ResourceExhausted, status.Code(err))
		})
	}
}
