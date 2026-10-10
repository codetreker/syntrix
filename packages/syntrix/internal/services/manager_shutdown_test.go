package services

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/codetreker/syntrix/internal/core/storage"
	storageconfig "github.com/codetreker/syntrix/internal/core/storage/config"
	identityconfig "github.com/codetreker/syntrix/internal/identity/config"
	"github.com/codetreker/syntrix/internal/indexer"
	indexer_config "github.com/codetreker/syntrix/internal/indexer/config"
	"github.com/codetreker/syntrix/internal/puller"
	puller_config "github.com/codetreker/syntrix/internal/puller/config"
	"github.com/codetreker/syntrix/internal/server"
	"github.com/codetreker/syntrix/internal/streamer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_Init_Start_Shutdown_NoServices(t *testing.T) {
	cfg := config.LoadConfig()
	opts := Options{}
	mgr := NewManager(cfg, opts)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	assert.NoError(t, mgr.Init(ctx))

	bgCtx, bgCancel := context.WithCancel(context.Background())
	mgr.Start(bgCtx)
	bgCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	mgr.Shutdown(shutdownCtx)
}

func TestManager_Shutdown_StreamerCloseError(t *testing.T) {
	setupManagerFactories(t)
	wantErr := errors.New("remote close error")
	mgr := NewManager(config.LoadConfig(), Options{})
	mgr.backends = &storage.Backends{}
	mgr.streamerClient = &mockStreamerClient{closeErr: wantErr}
	require.ErrorIs(t, mgr.shutdown(context.Background()), wantErr)
}

func managerTestBackends(t *testing.T) *storage.Backends {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backends, err := storage.NewBackends(ctx, config.LoadConfig().Storage)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backends.Close()) })
	return backends
}

type shutdownProbePuller struct {
	puller.LocalService
	stop func(context.Context) error
}

func (s *shutdownProbePuller) Stop(ctx context.Context) error { return s.stop(ctx) }

func TestManager_InitFailure_CleansUpWithIndependentContext(t *testing.T) {
	setupManagerFactories(t)
	server.InitDefault(server.DefaultConfig(), nil)
	backends := managerTestBackends(t)
	wantInitErr := errors.New("identity initialization failed")
	wantStopErr := errors.New("puller stop failed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	storageBackendsFactory = func(context.Context, *config.Config) (*storage.Backends, error) { return backends, nil }
	identityModuleFactory = func(context.Context, identityconfig.Config, storageconfig.Config, *storage.Backends) (identityModule, error) {
		cancel()
		return nil, wantInitErr
	}
	mgr := NewManager(config.LoadConfig(), Options{RunAPI: true})
	stopCount := 0
	mgr.pullerService = &shutdownProbePuller{stop: func(cleanupCtx context.Context) error {
		stopCount++
		require.NoError(t, cleanupCtx.Err())
		require.NoError(t, backends.PostgresDB().PingContext(cleanupCtx))
		if stopCount == 1 {
			return wantStopErr
		}
		return nil
	}}
	err := mgr.Init(ctx)
	require.ErrorIs(t, err, wantInitErr)
	require.ErrorIs(t, err, wantStopErr)
	require.Nil(t, mgr.identityModule)
	require.NoError(t, backends.PostgresDB().PingContext(context.Background()))
	require.NoError(t, mgr.shutdown(context.Background()))
	require.Equal(t, 2, stopCount)
	require.ErrorContains(t, backends.PostgresDB().PingContext(context.Background()), "database is closed")
}

func TestManager_ShutdownTimeout_RetainsBackendsUntilRetry(t *testing.T) {
	server.InitDefault(server.DefaultConfig(), nil)
	backends := managerTestBackends(t)
	mgr := NewManager(config.LoadConfig(), Options{})
	mgr.backends = backends
	mgr.wg.Add(1)
	finished := false
	t.Cleanup(func() {
		if !finished {
			mgr.wg.Done()
		}
	})
	provider := &stubPubSubProvider{}
	client := &mockStreamerClient{}
	mgr.pubsubProvider = provider
	mgr.streamerClient = client
	stopCount := 0
	mgr.pullerService = &shutdownProbePuller{stop: func(ctx context.Context) error {
		stopCount++
		require.NoError(t, backends.PostgresDB().PingContext(context.Background()))
		return nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, mgr.shutdown(ctx), context.DeadlineExceeded)
	require.NoError(t, backends.PostgresDB().PingContext(context.Background()))
	mgr.wg.Done()
	finished = true
	require.NoError(t, mgr.shutdown(context.Background()))
	require.NoError(t, mgr.shutdown(context.Background()))
	require.ErrorContains(t, backends.PostgresDB().PingContext(context.Background()), "database is closed")
	require.Equal(t, 1, client.closeCount)
	require.Equal(t, 1, provider.closeCount)
	require.Equal(t, 1, stopCount)
}

func TestManager_Shutdown_RemoteCloseFailureStillReleasesQuiescentBackends(t *testing.T) {
	server.InitDefault(server.DefaultConfig(), nil)
	backends := managerTestBackends(t)
	wantErr := errors.New("remote close failed")
	client := &mockStreamerClient{closeErr: wantErr}
	mgr := NewManager(config.LoadConfig(), Options{})
	mgr.backends = backends
	mgr.streamerClient = client
	require.ErrorIs(t, mgr.shutdown(context.Background()), wantErr)
	require.ErrorContains(t, backends.PostgresDB().PingContext(context.Background()), "database is closed")
	client.closeErr = nil
	require.NoError(t, mgr.shutdown(context.Background()))
	require.NoError(t, mgr.shutdown(context.Background()))
	require.Equal(t, 2, client.closeCount)
}

func TestManager_Shutdown_Timeout(t *testing.T) {
	mgr := &Manager{}
	mgr.wg.Add(1)
	defer mgr.wg.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	require.ErrorIs(t, mgr.shutdown(ctx), context.DeadlineExceeded)
}

type stubPubSubProvider struct {
	closed     bool
	closeCount int
}

func (s *stubPubSubProvider) NewPublisher(opts pubsub.PublisherOptions) (pubsub.Publisher, error) {
	return nil, nil
}
func (s *stubPubSubProvider) NewConsumer(opts pubsub.ConsumerOptions) (pubsub.Consumer, error) {
	return nil, nil
}
func (s *stubPubSubProvider) Close() error {
	s.closed = true
	s.closeCount++
	return nil
}

func TestManager_Shutdown_PullerAndPubSub(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{})

	provider := &stubPubSubProvider{}
	pullerSvc := &stubPullerService{}
	mgr.pubsubProvider = provider
	mgr.pullerService = pullerSvc
	mgr.pullerGRPC = puller.NewGRPCServerWithInit(puller_config.GRPCConfig{MaxConnections: 10}, pullerSvc, nil)

	mgr.Shutdown(context.Background())

	assert.True(t, provider.closed)
	assert.Equal(t, int32(1), pullerSvc.stopCount.Load())
}

// mockStreamerClient implements streamer.Service and io.Closer
type mockStreamerClient struct {
	closed     bool
	closeCount int
	closeErr   error
	streamErr  error
}

func (m *mockStreamerClient) Stream(ctx context.Context) (streamer.Stream, error) {
	return nil, m.streamErr
}

func (m *mockStreamerClient) Close() error {
	m.closed = true
	m.closeCount++
	return m.closeErr
}

func TestManager_Shutdown_StreamerClient(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{})

	mockClient := &mockStreamerClient{}
	mgr.streamerClient = mockClient

	mgr.Shutdown(context.Background())

	assert.True(t, mockClient.closed)
}

func TestManager_Shutdown_StreamerClientError(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{})

	mockClient := &mockStreamerClient{closeErr: errors.New("close error")}
	mgr.streamerClient = mockClient

	// Should not panic, just log the error
	mgr.Shutdown(context.Background())

	assert.True(t, mockClient.closed)
}

func TestManager_Shutdown_IndexerService(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunIndexer: true})

	// Use a real indexer service since LocalService has internal types
	mockIndexer, err := indexer.NewService(indexer_config.Config{}, nil, slog.Default())
	if err != nil {
		t.Fatalf("failed to create indexer service: %v", err)
	}
	mgr.indexerService = mockIndexer

	// Should not panic and should stop the indexer
	mgr.Shutdown(context.Background())
}
