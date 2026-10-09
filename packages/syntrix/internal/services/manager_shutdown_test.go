package services

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/core/database"
	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/internal/indexer"
	indexer_config "github.com/codetreker/syntrix/internal/indexer/config"
	"github.com/codetreker/syntrix/internal/puller"
	puller_config "github.com/codetreker/syntrix/internal/puller/config"
	"github.com/codetreker/syntrix/internal/streamer"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
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

type MockStorageFactory struct {
	mock.Mock
}

func (m *MockStorageFactory) Document() types.DocumentStore {
	return nil
}
func (m *MockStorageFactory) User() types.UserStore {
	return nil
}
func (m *MockStorageFactory) Revocation() types.TokenRevocationStore {
	return nil
}
func (m *MockStorageFactory) Database() database.DatabaseStore {
	return nil
}
func (m *MockStorageFactory) GetMongoClient(name string) (*mongo.Client, string, error) {
	return nil, "", nil
}
func (m *MockStorageFactory) Close() error {
	args := m.Called()
	return args.Error(0)
}

func TestManager_Shutdown_StorageError(t *testing.T) {
	// Override storage factory
	originalFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = originalFactory }()

	mockFactory := new(MockStorageFactory)
	mockFactory.On("Close").Return(errors.New("storage close error"))

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return mockFactory, nil
	}

	cfg := config.LoadConfig()
	opts := Options{
		RunQuery: true,
	}
	mgr := NewManager(cfg, opts)

	ctx := context.Background()
	// Init to set storageFactory
	err := mgr.Init(ctx)
	require.NoError(t, err)

	// Shutdown
	mgr.Shutdown(ctx)

	mockFactory.AssertExpectations(t)
}

func TestManager_Shutdown_Timeout(t *testing.T) {
	mgr := &Manager{}
	mgr.wg.Add(1) // Simulate a running task that never finishes

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// Should timeout and log "Timeout waiting for background tasks."
	mgr.Shutdown(ctx)
}

type stubPubSubProvider struct {
	closed bool
}

func (s *stubPubSubProvider) NewPublisher(opts pubsub.PublisherOptions) (pubsub.Publisher, error) {
	return nil, nil
}
func (s *stubPubSubProvider) NewConsumer(opts pubsub.ConsumerOptions) (pubsub.Consumer, error) {
	return nil, nil
}
func (s *stubPubSubProvider) Close() error {
	s.closed = true
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
	closed    bool
	closeErr  error
	streamErr error
}

func (m *mockStreamerClient) Stream(ctx context.Context) (streamer.Stream, error) {
	return nil, m.streamErr
}

func (m *mockStreamerClient) Close() error {
	m.closed = true
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
