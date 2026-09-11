package indexer

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/puller"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type recoveryValidationPuller struct {
	*recoveryBootstrapPuller
	failures  chan error
	validated chan string
}

func (p *recoveryValidationPuller) ValidateBoundary(ctx context.Context, progress string) error {
	select {
	case p.validated <- progress:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-p.failures:
		return err
	default:
		return nil
	}
}

func recoveryContractService(t *testing.T, ctx context.Context) (*service, *recoveryValidationPuller) {
	t.Helper()
	p := &recoveryValidationPuller{
		recoveryBootstrapPuller: &recoveryBootstrapPuller{bootstrapPuller: bootstrapPuller{marker: "opaque/bootstrap:empty"}, connections: make(chan recoverySubscription, 4)},
		failures:                make(chan error, 16),
		validated:               make(chan string, 32),
	}
	s := newBootstrapTestService(t, config.StorageModeMemory, &p.bootstrapPuller)
	s.pullerSvc = p
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, s.Stop(stopCtx))
	})
	source := &bootstrapDocuments{}
	require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	return s, p
}

func nextRecoveryConnection(t *testing.T, ctx context.Context, p *recoveryValidationPuller) recoverySubscription {
	t.Helper()
	select {
	case connection := <-p.connections:
		return connection
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return recoverySubscription{}
	}
}

func TestRecoveryValidatesAppliedBoundaryBeforeReattaching(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		failure   error
		retryable bool
	}{
		{"native-unavailable", fmt.Errorf("source: %w", puller.ErrCaptureUnavailable), true},
		{"native-deadline", context.DeadlineExceeded, true},
		{"grpc-unavailable", status.Error(codes.Unavailable, "reconnecting"), true},
		{"grpc-deadline", status.Error(codes.DeadlineExceeded, "timeout"), true},
		{"grpc-capacity", status.Error(codes.ResourceExhausted, "busy"), true},
		{"history-expired", status.Error(codes.FailedPrecondition, "history expired"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			s, p := recoveryContractService(t, ctx)
			require.NoError(t, s.Start(ctx))
			first := nextRecoveryConnection(t, ctx, p)
			first.events <- &puller.Event{Ready: true, Progress: first.after}
			require.NoError(t, s.WaitReady(ctx))
			for len(p.validated) > 0 {
				<-p.validated
			}
			const applied = "opaque/applied:position-with-empty-backend"
			doc := types.NewStoredDoc("db", "users/a/docs", "one", map[string]any{"score": int64(1)})
			first.events <- &puller.Event{Change: &ChangeEvent{Database: "db", FullDocument: &doc}, Progress: applied}
			p.failures <- tc.failure
			first.events <- &puller.Event{Error: status.Error(codes.Unavailable, "transport lost"), Retryable: true}
			require.Eventually(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.ready }, time.Second, time.Millisecond)
			if !tc.retryable {
				require.ErrorIs(t, s.WaitReady(ctx), tc.failure)
				require.Empty(t, p.connections)
			} else {
				second := nextRecoveryConnection(t, ctx, p)
				require.Equal(t, applied, second.after)
				_, err := s.OpenCandidates(ctx, "db", Plan{Collection: doc.Collection})
				require.ErrorIs(t, err, ErrIndexNotReady)
				second.events <- &puller.Event{Ready: true, Progress: second.after}
				require.NoError(t, s.WaitReady(ctx))
			}
			progress, err := s.store.LoadProgress()
			require.NoError(t, err)
			require.Equal(t, applied, progress)
			checks := 0
			for len(p.validated) > 0 {
				require.Equal(t, applied, <-p.validated)
				checks++
			}
			if tc.retryable {
				require.Equal(t, 2, checks)
			} else {
				require.Equal(t, 1, checks)
			}
		})
	}
}

func TestRecoveryExhaustionLeavesQueriesUnavailable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, p := recoveryContractService(t, ctx)
	require.NoError(t, s.Start(ctx))
	first := nextRecoveryConnection(t, ctx, p)
	first.events <- &puller.Event{Ready: true, Progress: first.after}
	require.NoError(t, s.WaitReady(ctx))
	for i := 0; i < 10; i++ {
		p.failures <- puller.ErrCaptureUnavailable
	}
	close(first.events)
	require.Eventually(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.ready }, time.Second, time.Millisecond)
	err := s.WaitReady(ctx)
	require.ErrorIs(t, err, puller.ErrCaptureUnavailable)
	require.ErrorContains(t, err, "attempts exhausted")
	require.Empty(t, p.connections)
	_, err = s.Search(ctx, "db", Plan{Collection: "users/a/docs"})
	require.ErrorIs(t, err, ErrIndexNotReady)
}

func TestMalformedRecoveryControlsNeverPublishReadyOrProgress(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		event *puller.Event
		cause string
	}{
		{"nil", nil, "nil event"},
		{"error-with-ready", &puller.Event{Error: errors.New("failure"), Ready: true}, "invalid Puller error control"},
		{"error-with-change", &puller.Event{Error: errors.New("failure"), Change: &ChangeEvent{}}, "invalid Puller error control"},
		{"ready-with-change", &puller.Event{Ready: true, Change: &ChangeEvent{}, Progress: "wrong"}, "invalid Puller readiness"},
		{"ready-without-progress", &puller.Event{Ready: true}, "invalid Puller readiness"},
		{"ready-ahead-of-applied", &puller.Event{Ready: true, Progress: "not-applied"}, "differs from applied progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, p := recoveryContractService(t, ctx)
			require.NoError(t, s.Start(ctx))
			connection := nextRecoveryConnection(t, ctx, p)
			connection.events <- tc.event
			require.ErrorContains(t, s.WaitReady(ctx), tc.cause)
			progress, err := s.store.LoadProgress()
			require.NoError(t, err)
			require.Equal(t, p.marker, progress)
			require.Empty(t, p.connections)
			_, err = s.Search(ctx, "db", Plan{Collection: "users/a/docs"})
			require.ErrorIs(t, err, ErrIndexNotReady)
		})
	}
}

type recoveryStorageFailure struct {
	store.Store
	armed    atomic.Bool
	flushErr error
	loadErr  error
}

func (s *recoveryStorageFailure) Flush() error {
	if s.armed.Load() && s.flushErr != nil {
		return s.flushErr
	}
	return s.Store.Flush()
}

func (s *recoveryStorageFailure) LoadProgress() (string, error) {
	if s.armed.Load() && s.loadErr != nil {
		return "", s.loadErr
	}
	return s.Store.LoadProgress()
}

func TestRecoveryStorageFailuresStopBeforeReadinessOrReattach(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"ready-flush", "recovery-flush", "recovery-load"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s, p := recoveryContractService(t, ctx)
			failure := errors.New("durability unavailable")
			failing := &recoveryStorageFailure{Store: s.store}
			if phase == "recovery-load" {
				failing.loadErr = failure
			} else {
				failing.flushErr = failure
			}
			s.store = failing
			require.NoError(t, s.Start(ctx))
			connection := nextRecoveryConnection(t, ctx, p)
			if phase != "ready-flush" {
				connection.events <- &puller.Event{Ready: true, Progress: connection.after}
				require.NoError(t, s.WaitReady(ctx))
			}
			failing.armed.Store(true)
			if phase == "ready-flush" {
				connection.events <- &puller.Event{Ready: true, Progress: connection.after}
			} else {
				connection.events <- &puller.Event{Error: puller.ErrCaptureUnavailable, Retryable: true}
			}
			require.Eventually(t, func() bool {
				s.mu.RLock()
				defer s.mu.RUnlock()
				return errors.Is(s.lifecycleErr, failure) && !s.retrying
			}, time.Second, time.Millisecond)
			require.ErrorIs(t, s.WaitReady(ctx), failure)
			require.Empty(t, p.connections)
			progress, err := failing.Store.LoadProgress()
			require.NoError(t, err)
			require.Equal(t, p.marker, progress)
		})
	}
}
