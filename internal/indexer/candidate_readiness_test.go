package indexer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/puller"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func requireCandidatesUnavailable(t *testing.T, ctx context.Context, local, remote CandidateService, plan Plan) {
	t.Helper()
	for _, service := range []CandidateService{local, remote} {
		stream, err := service.OpenCandidates(ctx, "db", plan)
		if stream != nil {
			require.NoError(t, stream.Close())
		}
		require.ErrorIs(t, err, ErrIndexNotReady)
	}
}

func requireCandidateRows(t *testing.T, ctx context.Context, local, remote CandidateService, plan Plan, ids []string) {
	t.Helper()
	for _, service := range []CandidateService{local, remote} {
		stream, err := service.OpenCandidates(ctx, "db", plan)
		require.NoError(t, err)
		defer stream.Close()
		var actual []string
		for {
			group, ok, err := stream.Next()
			require.NoError(t, err)
			if !ok {
				break
			}
			actual = append(actual, group.ID)
		}
		require.Equal(t, ids, actual)
		require.NoError(t, stream.Close())
	}
}

func TestCandidateReadinessBeforeServiceStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &bootstrapPuller{marker: bootstrapMarker(""), stream: make(chan *puller.Event)}
	s := newBootstrapTestService(t, config.StorageModeMemory, p)
	t.Cleanup(func() { require.NoError(t, s.Stop(context.Background())) })
	source := bootstrapTestDocuments()
	require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	remote := statsRemoteClient(t, s)
	plan := Plan{Collection: "users/a/docs", Filters: []Filter{{Field: "score", Op: FilterGte, Value: 0}}}
	stream, err := s.manager.OpenCandidates(ctx, "db", plan)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	requireCandidatesUnavailable(t, ctx, s, remote, plan)
	require.NoError(t, s.Start(ctx))
	require.NoError(t, s.WaitReady(ctx))
	requireCandidateRows(t, ctx, s, remote, plan, []string{"one"})
}

func TestCandidateReadinessWaitsForReplayProjection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &bootstrapPuller{marker: bootstrapMarker(""), target: bootstrapMarker("1-1-a"), stream: make(chan *puller.Event, 1)}
	s := newBootstrapTestService(t, config.StorageModeMemory, p)
	t.Cleanup(func() { require.NoError(t, s.Stop(context.Background())) })
	source := &bootstrapDocuments{}
	require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	remote := statsRemoteClient(t, s)
	blocked := &blockedProjectionStore{Store: s.store, entered: make(chan struct{}), release: make(chan struct{})}
	s.store = blocked
	release := sync.OnceFunc(func() { close(blocked.release) })
	t.Cleanup(release)
	doc := types.NewStoredDoc("db", "users/a/docs", "one", map[string]any{"score": int64(1)})
	p.stream <- &puller.Event{Change: &ChangeEvent{Database: "db", FullDocument: &doc}, Progress: p.target}
	require.NoError(t, s.Start(ctx))
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	plan := Plan{Collection: doc.Collection, Filters: []Filter{{Field: "score", Op: FilterGte, Value: 0}}}
	requireCandidatesUnavailable(t, ctx, s, remote, plan)
	release()
	require.NoError(t, s.WaitReady(ctx))
	requireCandidateRows(t, ctx, s, remote, plan, []string{"one"})
}

func TestCandidateReadinessAfterTransportRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &recoveryBootstrapPuller{bootstrapPuller: bootstrapPuller{marker: "opaque-bootstrap"}, connections: make(chan recoverySubscription, 4)}
	s := newBootstrapTestService(t, config.StorageModeMemory, &p.bootstrapPuller)
	s.pullerSvc = p
	t.Cleanup(func() { require.NoError(t, s.Stop(context.Background())) })
	source := bootstrapTestDocuments()
	require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	remote := statsRemoteClient(t, s)
	require.NoError(t, s.Start(ctx))
	var first recoverySubscription
	select {
	case first = <-p.connections:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	first.events <- &puller.Event{Ready: true, Progress: first.after}
	require.NoError(t, s.WaitReady(ctx))
	plan := Plan{Collection: "users/a/docs", Filters: []Filter{{Field: "score", Op: FilterGte, Value: 0}}}
	requireCandidateRows(t, ctx, s, remote, plan, []string{"one"})
	doc := types.NewStoredDoc("db", plan.Collection, "two", map[string]any{"score": int64(2)})
	first.events <- &puller.Event{Change: &ChangeEvent{Database: "db", FullDocument: &doc}, Progress: "opaque-applied"}
	first.events <- &puller.Event{Error: status.Error(codes.Unavailable, "transport disconnected"), Retryable: true}
	var second recoverySubscription
	select {
	case second = <-p.connections:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Equal(t, "opaque-applied", second.after)
	requireCandidatesUnavailable(t, ctx, s, remote, plan)
	replayed := types.NewStoredDoc("db", plan.Collection, "three", map[string]any{"score": int64(3)})
	second.events <- &puller.Event{Change: &ChangeEvent{Database: "db", FullDocument: &replayed}, Progress: "opaque-replayed"}
	require.Eventually(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.progress == "opaque-replayed"
	}, time.Second, time.Millisecond)
	requireCandidatesUnavailable(t, ctx, s, remote, plan)
	second.events <- &puller.Event{Ready: true, Progress: "opaque-replayed"}
	require.NoError(t, s.WaitReady(ctx))
	requireCandidateRows(t, ctx, s, remote, plan, []string{"one", "two", "three"})
}
