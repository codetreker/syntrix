package services

import (
	"context"
	"errors"
	gatewayconfig "github.com/codetreker/syntrix/internal/gateway/config"
	"github.com/codetreker/syntrix/internal/gateway/realtime"
	"github.com/codetreker/syntrix/internal/streamer"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/server"
	"github.com/stretchr/testify/require"
)

type unsettledTask struct {
	err, closeErr     error
	shutdowns, closed int
}

func (*unsettledTask) Start(ctx context.Context) error  { <-ctx.Done(); return nil }
func (s *unsettledTask) Shutdown(context.Context) error { s.shutdowns++; return s.err }
func (s *unsettledTask) Close() error                   { s.closed++; return s.closeErr }

type unsettledServer struct {
	server.Service
	err error
}

func (s *unsettledServer) Stop(context.Context) error { return s.err }

type unsettledStreamer struct {
	readyStreamer
	err error
}

func (s *unsettledStreamer) Stop(context.Context) error { return s.err }

func TestShutdownSettlesTriggerOwnersBeforeClosingBorrowedResources(t *testing.T) {
	previous := server.Default()
	server.SetDefault(nil)
	t.Cleanup(func() { server.SetDefault(previous) })
	for _, role := range []string{roleEvaluator, roleWorker} {
		t.Run(role, func(t *testing.T) {
			mgr := NewManager(config.DefaultConfig(), Options{})
			mgr.backends = managerTestBackends(t)
			provider := &stubPubSubProvider{}
			mgr.pubsubProvider = provider
			owner := &unsettledTask{err: errors.New("workers are not settled")}
			if role == roleEvaluator {
				mgr.triggerService = owner
			} else {
				mgr.triggerConsumer = owner
			}
			require.ErrorIs(t, mgr.shutdown(context.Background()), owner.err)
			require.NoError(t, mgr.backends.PostgresDB().PingContext(context.Background()))
			require.Zero(t, provider.closeCount)
			require.Zero(t, owner.closed)
			owner.err = nil
			require.NoError(t, mgr.shutdown(context.Background()))
			require.Equal(t, 1, provider.closeCount)
			require.Equal(t, 2, owner.shutdowns)
			require.Error(t, mgr.backends.PostgresDB().PingContext(context.Background()))
		})
	}
}

func TestShutdownPreservesUnsettledServerAndStreamerForRetry(t *testing.T) {
	previous := server.Default()
	t.Cleanup(func() { server.SetDefault(previous) })
	for _, role := range []string{"server", "streamer"} {
		t.Run(role, func(t *testing.T) {
			cause := errors.New("owner stop failed")
			mgr := NewManager(config.DefaultConfig(), Options{})
			mgr.backends = managerTestBackends(t)
			provider := &stubPubSubProvider{}
			mgr.pubsubProvider = provider
			network := &unsettledServer{}
			server.SetDefault(network)
			ingest := &unsettledStreamer{}
			mgr.streamerService = ingest
			if role == "server" {
				network.err = cause
			} else {
				ingest.err = cause
			}
			require.ErrorIs(t, mgr.shutdown(context.Background()), cause)
			require.NoError(t, mgr.backends.PostgresDB().PingContext(context.Background()))
			require.Zero(t, provider.closeCount)
			network.err = nil
			ingest.err = nil
			require.NoError(t, mgr.shutdown(context.Background()))
			require.Equal(t, 1, provider.closeCount)
		})
	}
}

func TestShutdownReportsSettledEvaluatorReleaseFailure(t *testing.T) {
	previous := server.Default()
	server.SetDefault(nil)
	t.Cleanup(func() { server.SetDefault(previous) })
	cause := errors.New("publisher close failed")
	mgr := NewManager(config.DefaultConfig(), Options{})
	owner := &unsettledTask{closeErr: cause}
	mgr.triggerService = owner
	require.ErrorIs(t, mgr.shutdown(context.Background()), cause)
	owner.closeErr = nil
	require.NoError(t, mgr.shutdown(context.Background()))
	require.Equal(t, 2, owner.closed)
	require.Nil(t, mgr.triggerService)
}

type unsettledRealtimeStream struct {
	readyStream
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *unsettledRealtimeStream) Recv() (*streamer.EventDelivery, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return nil, io.EOF
}
func (*unsettledRealtimeStream) Close() error { return nil }

type unsettledRealtimeSource struct{ stream streamer.Stream }

func (s unsettledRealtimeSource) Stream(context.Context) (streamer.Stream, error) {
	return s.stream, nil
}

func TestShutdownKeepsBorrowedBackendsUntilRealtimeReceivesHaveSettled(t *testing.T) {
	previous := server.Default()
	server.SetDefault(nil)
	t.Cleanup(func() { server.SetDefault(previous) })
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	wire := &unsettledRealtimeStream{entered: make(chan struct{}), release: release}
	source := unsettledRealtimeSource{stream: wire}
	mgr := NewManager(config.DefaultConfig(), Options{})
	mgr.backends = managerTestBackends(t)
	provider := &stubPubSubProvider{}
	mgr.pubsubProvider = provider
	mgr.rtServer = realtime.NewServer(&MockQueryService{}, source, "documents", &MockAuthService{}, gatewayconfig.RealtimeConfig{})
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, mgr.rtServer.StartBackgroundTasks(life))
	select {
	case <-wire.entered:
	case <-time.After(time.Second):
		t.Fatal("realtime receiver never started")
	}
	deadline, deadlineCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	require.ErrorIs(t, mgr.shutdown(deadline), context.DeadlineExceeded)
	deadlineCancel()
	require.Zero(t, provider.closeCount)
	require.NoError(t, mgr.backends.PostgresDB().PingContext(context.Background()))
	unblock()
	retry, retryCancel := context.WithTimeout(context.Background(), time.Second)
	defer retryCancel()
	require.NoError(t, mgr.shutdown(retry))
	require.Equal(t, 1, provider.closeCount)
	require.Error(t, mgr.backends.PostgresDB().PingContext(context.Background()))
}

type unsettledDeletionWorker struct {
	mockDeletionWorker
	stopErr error
	stops   int
}

func (s *unsettledDeletionWorker) Stop(context.Context) error { s.stops++; return s.stopErr }

func TestShutdownKeepsResourcesUntilDeletionWorkerStops(t *testing.T) {
	previous := server.Default()
	server.SetDefault(nil)
	t.Cleanup(func() { server.SetDefault(previous) })
	cause := errors.New("deletion worker stop failed")
	mgr := NewManager(config.DefaultConfig(), Options{})
	mgr.backends = managerTestBackends(t)
	provider := &stubPubSubProvider{}
	mgr.pubsubProvider = provider
	worker := &unsettledDeletionWorker{stopErr: cause}
	mgr.deletionWorker = worker
	require.ErrorIs(t, mgr.shutdown(context.Background()), cause)
	require.Same(t, worker, mgr.deletionWorker)
	require.Zero(t, provider.closeCount)
	require.NoError(t, mgr.backends.PostgresDB().PingContext(context.Background()))
	worker.stopErr = nil
	require.NoError(t, mgr.shutdown(context.Background()))
	require.Nil(t, mgr.deletionWorker)
	require.Equal(t, 2, worker.stops)
	require.Equal(t, 1, provider.closeCount)
	require.Error(t, mgr.backends.PostgresDB().PingContext(context.Background()))
}
