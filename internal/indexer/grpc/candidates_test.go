package grpc

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	indexerv1 "github.com/syntrixbase/syntrix/api/gen/indexer/v1"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	indexerclient "github.com/syntrixbase/syntrix/internal/indexer/client"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"log/slog"
)

type readyCandidateService struct {
	*mockLocalService
	observeContext func(context.Context)
}

func (s *readyCandidateService) OpenCandidates(ctx context.Context, database string, plan manager.Plan) (manager.CandidateStream, error) {
	if s.observeContext != nil {
		s.observeContext(ctx)
	}
	return s.mgr.OpenCandidates(ctx, database, plan)
}

type candidateLogBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *candidateLogBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(value)
}
func (b *candidateLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type candidateContextStream struct{ googlegrpc.ServerStream }

func (candidateContextStream) Context() context.Context { return context.Background() }
func (candidateContextStream) Send(*indexerv1.CandidateResponse) error {
	return fmt.Errorf("unexpected candidate response")
}

func TestCandidateWireParity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, ctxkeys.KeyRequestID, "wire-request-1")
	observedContext := make(chan string, 16)
	logs := &candidateLogBuffer{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previousLogger)
	st := mem_store.New()
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	mgr := manager.New(st)
	require.NoError(t, mgr.LoadTemplatesFromBytes([]byte(`templates:
  - name: score
    collectionPattern: items
    includeDeleted: true
    fields:
      - {field: score, order: asc}
`)))
	tmpl := mgr.Templates()[0]
	ref := store.QueryIndexRef{Database: "db", Collection: "items", TemplateFingerprint: tmpl.Fingerprint(), Generation: "build-1"}
	for i, value := range []any{float64(1), int64(1<<53 + 1), int64(math.MaxInt64)} {
		id := fmt.Sprintf("doc-%d", i)
		key, err := encoding.Encode([]encoding.Field{{Value: value, Direction: encoding.Asc}}, id)
		require.NoError(t, err)
		require.NoError(t, st.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: id, PostingKeys: [][]byte{key}}}, ""))
	}
	require.NoError(t, st.PublishGeneration(ref, "bootstrapped"))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := googlegrpc.NewServer()
	indexerv1.RegisterIndexerServiceServer(server, NewServer(&readyCandidateService{mockLocalService: &mockLocalService{mgr: mgr}, observeContext: func(ctx context.Context) {
		requestID, _ := ctx.Value(ctxkeys.KeyRequestID).(string)
		observedContext <- requestID
	}}))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	client, err := indexerclient.New(lis.Addr().String(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	plan := manager.Plan{Collection: "items", ShowDeleted: true, MaxExamined: 100,
		OrderBy: []manager.OrderField{{Field: "score", Direction: encoding.Asc}},
		Filters: []manager.Filter{{Field: "score", Op: manager.FilterGte, Value: int64(1<<53 + 1)}},
	}
	local, err := mgr.OpenCandidates(ctx, "db", plan)
	require.NoError(t, err)
	defer local.Close()
	remote, err := client.OpenCandidates(ctx, "db", plan)
	require.NoError(t, err)
	defer remote.Close()
	require.Equal(t, "wire-request-1", <-observedContext)
	assert.Equal(t, local.Metadata(), remote.Metadata())
	var first manager.CandidateGroup
	for i := 0; ; i++ {
		want, more, err := local.Next()
		require.NoError(t, err)
		got, remoteMore, err := remote.Next()
		require.NoError(t, err)
		assert.Equal(t, more, remoteMore)
		assert.Equal(t, want, got)
		assert.Equal(t, local.Examined(), remote.Examined())
		if !more {
			break
		}
		if i == 0 {
			first = got
			assert.Equal(t, "doc-1", got.ID)
		}
	}
	meta := remote.Metadata()
	plan.AfterPosition, plan.TemplateFingerprint = first.Position, meta.TemplateFingerprint
	plan.Generation, plan.BranchHash = meta.Generation, meta.BranchHash
	resumed, err := client.OpenCandidates(ctx, "db", plan)
	require.NoError(t, err)
	defer resumed.Close()
	got, more, err := resumed.Next()
	require.NoError(t, err)
	require.True(t, more)
	assert.Equal(t, "doc-2", got.ID)
	plan.Generation = "obsolete"
	_, err = client.OpenCandidates(ctx, "db", plan)
	assert.ErrorIs(t, err, manager.ErrStaleCursor)
	plan.Generation = meta.Generation
	plan.AfterPosition = nil
	plan.MaxExamined = 1
	limited, err := client.OpenCandidates(ctx, "db", plan)
	require.NoError(t, err)
	defer limited.Close()
	for i := 0; i < 3; i++ {
		_, more, err = limited.Next()
		if err != nil || !more {
			break
		}
	}
	assert.ErrorIs(t, err, store.ErrWorkLimit)
	require.Eventually(t, func() bool { return bytes.Contains([]byte(logs.String()), []byte(`"reason":"work_limit"`)) }, time.Second, time.Millisecond)
	output := logs.String()
	assert.Contains(t, output, `"msg":"Index candidates admitted"`)
	assert.Contains(t, output, `"request_id":"wire-request-1"`)
	assert.Contains(t, output, `"reason":"exhausted"`)
	assert.NotContains(t, output, "build-1")
	assert.NotContains(t, output, "doc-1")
	assert.NotContains(t, output, "9007199254740993")
}

func TestCandidateRequestTypedValues(t *testing.T) {
	for _, value := range []any{int64(math.MinInt64), int64(math.MaxInt64), float64(math.MaxInt64), []any{int64(1<<53 + 1), float64(1.5)}} {
		data, err := model.EncodeTypedValue(value)
		require.NoError(t, err)
		plan, err := candidateRequestToPlan(&indexerv1.CandidateRequest{Filters: []*indexerv1.CandidateFilter{{Field: "value", Op: "in", TypedValue: data}}})
		require.NoError(t, err)
		assert.Equal(t, value, plan.Filters[0].Value)
	}
	_, err := candidateRequestToPlan(&indexerv1.CandidateRequest{Filters: []*indexerv1.CandidateFilter{{TypedValue: []byte(`{"type":"int64","value":"9223372036854775808"}`)}}})
	require.Error(t, err)
}

func TestCandidateServiceRequired(t *testing.T) {
	st := mem_store.New()
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	server := NewServer(&mockLocalService{mgr: manager.New(st)})
	err := server.OpenCandidates(&indexerv1.CandidateRequest{Database: "db", Collection: "items"}, candidateContextStream{})
	result := status.Convert(err)
	require.Equal(t, codes.Unavailable, result.Code())
	require.Len(t, result.Details(), 1)
	info, ok := result.Details()[0].(*errdetails.ErrorInfo)
	require.True(t, ok)
	require.Equal(t, "INDEX_NOT_READY", info.Reason)
	require.Equal(t, "syntrix.indexer", info.Domain)
}

func TestCandidateTypedErrors(t *testing.T) {
	for _, tc := range []struct {
		err        error
		code       codes.Code
		reason     string
		completion string
	}{
		{manager.ErrStaleCursor, codes.FailedPrecondition, "STALE_CURSOR", "stale_cursor"},
		{store.ErrWorkLimit, codes.ResourceExhausted, "WORK_LIMIT", "work_limit"},
		{manager.ErrNoMatchingIndex, codes.NotFound, "NO_MATCHING_INDEX", "no_complete_index_plan"},
		{manager.ErrIndexNotReady, codes.Unavailable, "INDEX_NOT_READY", "index_unavailable"},
		{manager.ErrIndexRebuilding, codes.Unavailable, "INDEX_REBUILDING", "index_unavailable"},
		{manager.ErrInvalidPlan, codes.InvalidArgument, "INVALID_PLAN", "invalid_query"},
	} {
		st := status.Convert(candidateError(fmt.Errorf("wrapped: %w", tc.err)))
		assert.Equal(t, tc.code, st.Code())
		require.Len(t, st.Details(), 1)
		info, ok := st.Details()[0].(*errdetails.ErrorInfo)
		require.True(t, ok)
		assert.Equal(t, "syntrix.indexer", info.Domain)
		assert.Equal(t, tc.reason, info.Reason)
		assert.Equal(t, tc.completion, candidateCompletionReason(st.Err()))
	}
}

func TestCandidateCompletionReasonSanitizesTransportFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{nil, "exhausted"},
		{status.Error(codes.Canceled, "private cursor value"), "canceled"},
		{status.Error(codes.DeadlineExceeded, "private filter value"), "deadline_exceeded"},
		{fmt.Errorf("private source document"), "internal_error"},
	} {
		require.Equal(t, tc.reason, candidateCompletionReason(tc.err))
	}
	foreign, err := status.New(codes.Internal, "private source document").WithDetails(&errdetails.ErrorInfo{Domain: "untrusted", Reason: "STALE_CURSOR"})
	require.NoError(t, err)
	require.Equal(t, "internal_error", candidateCompletionReason(foreign.Err()))
}

type candidateSendProbe struct {
	candidateContextStream
	sends   int
	failure error
}

func (p *candidateSendProbe) Send(*indexerv1.CandidateResponse) error { p.sends++; return p.failure }

func TestCandidateSendPreservesByteLimitAndTransportFailure(t *testing.T) {
	probe := &candidateSendProbe{}
	response := &indexerv1.CandidateResponse{Payload: &indexerv1.CandidateResponse_Group{Group: &indexerv1.CandidateGroup{Id: "one", Position: bytes.Repeat([]byte{1}, candidateMessageLimit)}}}
	err := sendCandidate(probe, response)
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, "work_limit", candidateCompletionReason(err))
	require.Zero(t, probe.sends)
	failure := fmt.Errorf("transport send failed")
	probe.failure = failure
	response.GetGroup().Position = response.GetGroup().Position[:candidateMessageLimit-32]
	require.ErrorIs(t, sendCandidate(probe, response), failure)
	require.Equal(t, 1, probe.sends)
	probe.failure = nil
	require.NoError(t, sendCandidate(probe, response))
	require.Equal(t, 2, probe.sends)
}
