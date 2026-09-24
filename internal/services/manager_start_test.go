package services

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syntrixbase/syntrix/internal/config"
	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	api_config "github.com/syntrixbase/syntrix/internal/gateway/config"
	"github.com/syntrixbase/syntrix/internal/gateway/realtime"
	"github.com/syntrixbase/syntrix/internal/indexer"
	indexer_config "github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/puller"
	puller_config "github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/internal/server"
	"github.com/syntrixbase/syntrix/internal/server/ratelimit"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/internal/trigger"
	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.mongodb.org/mongo-driver/mongo"
	"google.golang.org/grpc"
)

type MockQueryService struct {
	mock.Mock
}

func (m *MockQueryService) GetDocument(ctx context.Context, database string, path string) (model.Document, error) {
	return nil, nil
}
func (m *MockQueryService) CreateDocument(ctx context.Context, database string, doc model.Document) error {
	return nil
}
func (m *MockQueryService) ReplaceDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	return nil, nil
}
func (m *MockQueryService) PatchDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	return nil, nil
}
func (m *MockQueryService) DeleteDocument(ctx context.Context, database string, path string, pred model.Filters) error {
	return nil
}
func (m *MockQueryService) ExecuteQuery(ctx context.Context, database string, q model.Query) ([]model.Document, error) {
	return nil, nil
}
func (m *MockQueryService) Pull(ctx context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	return nil, nil
}
func (m *MockQueryService) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	return nil, nil
}

// Removed tests that depended on WatchCollection and old Realtime retry logic
type storageBackendStub struct {
	watchCalls atomic.Int32
}

func (s *storageBackendStub) Get(context.Context, string, string) (*storage.StoredDoc, error) {
	return nil, model.ErrNotFound
}
func (s *storageBackendStub) Create(context.Context, string, *storage.StoredDoc) error { return nil }
func (s *storageBackendStub) Update(context.Context, string, string, map[string]interface{}, model.Filters) error {
	return nil
}
func (s *storageBackendStub) Patch(context.Context, string, string, map[string]interface{}, model.Filters) error {
	return nil
}
func (s *storageBackendStub) Delete(context.Context, string, string, model.Filters) error { return nil }
func (s *storageBackendStub) Query(context.Context, string, model.Query) ([]*storage.StoredDoc, error) {
	return nil, nil
}
func (s *storageBackendStub) Watch(context.Context, string, string, storage.WatchCheckpoint, storage.WatchOptions) (storage.WatchStream, error) {
	s.watchCalls.Add(1)
	return nil, nil
}
func (s *storageBackendStub) Close(context.Context) error { return nil }
func (s *storageBackendStub) DB() *mongo.Database         { return nil }

type triggerConsumerStub struct {
	called atomic.Int32
}

func (c *triggerConsumerStub) Start(ctx context.Context) error {
	c.called.Add(1)
	<-ctx.Done()
	return nil
}

type mockTriggerService struct {
	mock.Mock
}

func (m *mockTriggerService) Start(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockTriggerService) LoadTriggers(triggers []*trigger.Trigger) error {
	args := m.Called(triggers)
	return args.Error(0)
}

func TestManager_Start_TriggerEvaluator_CallsStart(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunTriggerEvaluator: true})

	mockTS := new(mockTriggerService)
	mgr.triggerService = mockTS

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockTS.On("Start", bgCtx).Return(nil)

	mgr.Start(bgCtx)

	// Wait for Start to be called
	time.Sleep(50 * time.Millisecond)
	mockTS.AssertExpectations(t)
}

func TestManager_Start_TriggerWorker_CallsStart(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunTriggerWorker: true})

	worker := &triggerConsumerStub{}
	mgr.triggerConsumer = worker

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(bgCtx)

	assert.Eventually(t, func() bool {
		return worker.called.Load() == 1
	}, 1*time.Second, 10*time.Millisecond, "Should call Start exactly once")
}

func TestManager_Start_PullerAndGRPC(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunPuller: true})

	pullerSvc := &stubPullerService{}
	mgr.pullerService = pullerSvc
	mgr.pullerGRPC = puller.NewGRPCServerWithInit(puller_config.GRPCConfig{MaxConnections: 10}, pullerSvc, nil)

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(bgCtx)
	mgr.Shutdown(context.Background())

	assert.Equal(t, int32(1), pullerSvc.startCount.Load())
}

type rtQueryStub struct {
	calls      atomic.Int32
	failFirst  bool
	failAlways bool
}

func (s *rtQueryStub) GetDocument(context.Context, string, string) (model.Document, error) {
	return model.Document{}, nil
}
func (s *rtQueryStub) CreateDocument(context.Context, string, model.Document) error { return nil }
func (s *rtQueryStub) ReplaceDocument(context.Context, string, model.Document, model.Filters) (model.Document, error) {
	return model.Document{}, nil
}
func (s *rtQueryStub) PatchDocument(context.Context, string, model.Document, model.Filters) (model.Document, error) {
	return model.Document{}, nil
}
func (s *rtQueryStub) DeleteDocument(context.Context, string, string, model.Filters) error {
	return nil
}
func (s *rtQueryStub) ExecuteQuery(context.Context, string, model.Query) ([]model.Document, error) {
	return nil, nil
}
func (s *rtQueryStub) Pull(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	return nil, nil
}
func (s *rtQueryStub) Push(context.Context, string, storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	return nil, nil
}

type stubPullerService struct {
	startCount atomic.Int32
	stopCount  atomic.Int32
	mu         sync.Mutex
	addCalls   []pullerBackendCall
}

type pullerBackendCall struct {
	name string
	db   string
}

func (s *stubPullerService) AddBackend(name string, _ *mongo.Client, dbName string, _ puller_config.PullerBackendConfig) error {
	s.mu.Lock()
	s.addCalls = append(s.addCalls, pullerBackendCall{name: name, db: dbName})
	s.mu.Unlock()
	return nil
}
func (s *stubPullerService) Start(ctx context.Context) error {
	s.startCount.Add(1)
	return nil
}
func (s *stubPullerService) Stop(ctx context.Context) error {
	s.stopCount.Add(1)
	return nil
}
func (s *stubPullerService) BackendNames() []string { return nil }
func (s *stubPullerService) SetEventHandler(func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error) {
}
func (s *stubPullerService) Replay(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
	return nil, nil
}
func (s *stubPullerService) ReplayFromAdmission(ctx context.Context, after map[string]string, firstBroadcast map[string]events.ClusterTime) (events.Iterator, error) {
	return nil, nil
}
func (s *stubPullerService) Subscribe(ctx context.Context, consumerID string, after string) <-chan *events.PullerEvent {
	return make(chan *events.PullerEvent)
}

func freeAddr() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "127.0.0.1:0"
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func waitForServer(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 100 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("server not reachable")
}

func TestManager_Start_AllServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{
		RunTriggerEvaluator: true,
		RunTriggerWorker:    true,
	})

	// 1. Mock Server Service (Global Default)
	mockSrv := new(MockServerService)
	// Start blocks, so we verify it was called and return nil.
	// Since it runs in a goroutine, it might race if we don't mock blocking,
	// but the test context cancellation should handle it.
	mockSrv.On("Start", mock.Anything).Return(nil).Maybe()
	server.SetDefault(mockSrv)
	defer server.SetDefault(nil)

	// 2. Mock Streamer Service
	mockStreamer := new(MockStreamerService)
	mockStreamer.On("Start", mock.Anything).Return(nil)

	mockStream := new(MockStream)
	// Recv blocks or returns error. Simulate cancellation or immediate return to avoid hanging?
	// Realtime's listener loop handles error and sleeps/retries or exits.
	// We return context canceled to stop the loop.
	mockStream.On("Recv").Return((*streamer.EventDelivery)(nil), context.Canceled)
	mockStream.On("Close").Return(nil).Maybe()
	// Realtime calls Stream(ctx)
	mockStreamer.On("Stream", mock.Anything).Return(mockStream, nil)
	mgr.streamerService = mockStreamer

	// 3. Mock Realtime Server
	mockQuery := new(MockQueryService)
	mockAuth := new(MockAuthService)
	rtCfg := api_config.RealtimeConfig{}
	// We need to pass the real realtime.Server to manager
	rtSrv := realtime.NewServer(mockQuery, mockStreamer, "data", mockAuth, rtCfg)
	mgr.rtServer = rtSrv

	// 4. Mock Trigger Service
	mockTrigger := new(MockTriggerService)
	mockTrigger.On("Start", mock.Anything).Return(nil)
	mgr.triggerService = mockTrigger

	// 5. Mock Trigger Consumer
	mockTriggerConsumer := new(MockTriggerConsumer)
	mockTriggerConsumer.On("Start", mock.Anything).Return(nil)
	mgr.triggerConsumer = mockTriggerConsumer

	// Start Manager
	// This launches multiple goroutines.
	mgr.Start(ctx)

	// Give time for goroutines to initialize
	time.Sleep(200 * time.Millisecond)

	// Verify expectations
	mockTrigger.AssertCalled(t, "Start", mock.Anything)
	mockTriggerConsumer.AssertCalled(t, "Start", mock.Anything)
	mockStreamer.AssertCalled(t, "Start", mock.Anything)

	// mockSrv.AssertCalled(t, "Start", mock.Anything) // Might not be called if server.Default() logic isn't hit or racy
	// Wait, we set SetDefault, so it SHOULD be called.
	// But it is behind if s := server.Default(); s != nil
	// We did SetDefault(mockSrv).
	mockSrv.AssertCalled(t, "Start", mock.Anything)
}

// --- Mocks ---

type MockServerService struct {
	mock.Mock
}

func (m *MockServerService) Start(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}
func (m *MockServerService) Stop(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}
func (m *MockServerService) RegisterHTTPHandler(pattern string, handler http.Handler) {
	m.Called(pattern, handler)
}
func (m *MockServerService) RegisterGRPCService(desc *grpc.ServiceDesc, impl interface{}) {
	m.Called(desc, impl)
}
func (m *MockServerService) HTTPMux() *http.ServeMux {
	m.Called()
	return http.NewServeMux()
}
func (m *MockServerService) AuthRateLimiter() ratelimit.Limiter {
	return nil
}

type MockStreamerService struct {
	mock.Mock
	streamCalls atomic.Int32
}

func (m *MockStreamerService) Start(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *MockStreamerService) Stop(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *MockStreamerService) Stream(ctx context.Context) (streamer.Stream, error) {
	m.streamCalls.Add(1)
	args := m.Called(ctx)
	if s := args.Get(0); s != nil {
		return s.(streamer.Stream), args.Error(1)
	}
	return nil, args.Error(1)
}

// MockStream implements streamer.Stream interface
type MockStream struct {
	mock.Mock
}

func (m *MockStream) Subscribe(ctx context.Context, database, collection string, filters []model.Filter) (streamer.Registration, error) {
	if err := ctx.Err(); err != nil {
		return streamer.Registration{}, err
	}
	args := m.Called(database, collection, filters)
	return streamer.Registration{ID: args.String(0), Generation: 1}, args.Error(1)
}
func (m *MockStream) Status() streamer.StreamStatus {
	return streamer.StreamStatus{State: streamer.StateConnected, Generation: 1}
}
func (m *MockStream) Unsubscribe(subscriptionID string) error {
	return m.Called(subscriptionID).Error(0)
}
func (m *MockStream) Recv() (*streamer.EventDelivery, error) {
	args := m.Called()
	if e := args.Get(0); e != nil {
		return e.(*streamer.EventDelivery), args.Error(1)
	}
	return nil, args.Error(1)
}
func (m *MockStream) Close() error {
	return m.Called().Error(0)
}

type MockTriggerService struct {
	mock.Mock
}

func (m *MockTriggerService) Start(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *MockTriggerService) LoadTriggers(triggers []*trigger.Trigger) error {
	return m.Called(triggers).Error(0)
}

type MockTriggerConsumer struct {
	mock.Mock
}

func (m *MockTriggerConsumer) Start(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

type MockAuthService struct {
	mock.Mock
}

func (m *MockAuthService) Middleware(next http.Handler) http.Handler {
	return next
}
func (m *MockAuthService) MiddlewareOptional(next http.Handler) http.Handler {
	return next
}
func (m *MockAuthService) SignIn(ctx context.Context, req identity.LoginRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (m *MockAuthService) SignUp(ctx context.Context, req identity.SignupRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (m *MockAuthService) Refresh(ctx context.Context, req identity.RefreshRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (m *MockAuthService) ListUsers(ctx context.Context, limit int, offset int) ([]*identity.User, error) {
	return nil, nil
}
func (m *MockAuthService) UpdateUser(ctx context.Context, id string, roles []string, dbAdmin []string, disabled bool) error {
	return nil
}
func (m *MockAuthService) Logout(ctx context.Context, refreshToken string) error {
	return nil
}
func (m *MockAuthService) GenerateSystemToken(serviceName string) (string, error) {
	return "", nil
}
func (m *MockAuthService) ValidateToken(tokenString string) (*identity.Claims, error) {
	return nil, nil
}

func TestManager_Start_RealtimeRetry(t *testing.T) {
	// Shorter timeout for retry test
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	// We don't defer cancel here immediately because we want to wait for timeout in the test logic naturally?
	// or we just let it run.
	defer cancel()

	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{})

	mockStreamer := new(MockStreamerService)
	// Expect Start to be called
	mockStreamer.On("Start", mock.Anything).Return(nil)
	// Always fail
	mockStreamer.On("Stream", mock.Anything).Return(nil, errors.New("connection failed"))
	mgr.streamerService = mockStreamer

	mockQuery := new(MockQueryService)
	mockAuth := new(MockAuthService)
	rtCfg := api_config.RealtimeConfig{}
	rtSrv := realtime.NewServer(mockQuery, mockStreamer, "data", mockAuth, rtCfg)
	mgr.rtServer = rtSrv

	// Start
	mgr.Start(ctx)

	// Wait for context timeout (200ms)
	<-ctx.Done()

	// Wait a bit for background goroutines to finish after context cancellation
	// This prevents race between goroutine calling Stream() and us reading Calls
	time.Sleep(100 * time.Millisecond)

	// Stream should have been called multiple times due to retry
	// 50ms initial sleep + 50ms retry wait.
	// 0ms: Start (sleep 50ms)
	// 50ms: Attempt 1 -> Stream -> Error -> Wait 50ms
	// 100ms: Attempt 2 -> Stream -> Error -> Wait 50ms
	// 150ms: Attempt 3 -> Stream -> Error -> Wait 50ms
	// 200ms: Context done.
	// Expect at least 2 calls.
	assert.GreaterOrEqual(t, int(mockStreamer.streamCalls.Load()), 2, "Should attempt retry at least once")
}

func TestManager_Start_IndexerService(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunIndexer: true})

	// Use a real indexer service instead of a stub since LocalService has internal types
	mockIndexer, err := indexer.NewService(indexer_config.Config{}, nil, slog.Default())
	if err != nil {
		t.Fatalf("failed to create indexer service: %v", err)
	}
	mgr.indexerService = mockIndexer

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(bgCtx)

	// Wait for Start to be called
	time.Sleep(100 * time.Millisecond)

	// Just verify it doesn't panic - the indexer was started
}

func TestManager_DatabaseService(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{})

	// Initially nil
	assert.Nil(t, mgr.DatabaseService())
}

// mockDeletionWorker implements deletionWorkerService for testing
type mockDeletionWorker struct {
	startCalled atomic.Int32
	stopCalled  atomic.Int32
	startErr    error
}

func (m *mockDeletionWorker) Start(ctx context.Context) error {
	m.startCalled.Add(1)
	return m.startErr
}

func (m *mockDeletionWorker) Stop(ctx context.Context) error {
	m.stopCalled.Add(1)
	return nil
}

func TestManager_Start_DeletionWorker(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{})

	worker := &mockDeletionWorker{}
	mgr.deletionWorker = worker

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(bgCtx)

	// Wait for deletion worker to start
	assert.Eventually(t, func() bool {
		return worker.startCalled.Load() >= 1
	}, 1*time.Second, 10*time.Millisecond, "Deletion worker should be started")
}

func TestManager_Start_DeletionWorker_Error(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{})

	worker := &mockDeletionWorker{startErr: errors.New("start failed")}
	mgr.deletionWorker = worker

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should not panic even if deletion worker fails
	mgr.Start(bgCtx)

	// Wait for attempt
	assert.Eventually(t, func() bool {
		return worker.startCalled.Load() >= 1
	}, 1*time.Second, 10*time.Millisecond, "Deletion worker start should be attempted")
}

// TestManager_Start_NilServices verifies that Start does not panic
// when services are nil. This is the expected behavior after refactoring
// to use nil checks instead of opts.RunXXX flags.
func TestManager_Start_NilServices(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{
		// Even with all RunXXX flags set, if services are nil, Start should not panic
		RunTriggerEvaluator: true,
		RunTriggerWorker:    true,
		RunPuller:           true,
		RunIndexer:          true,
		RunStreamer:         true,
		Mode:                ModeDistributed,
	})

	// All services are nil - this should not panic
	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start should complete without panic
	assert.NotPanics(t, func() {
		mgr.Start(bgCtx)
	}, "Start should not panic when services are nil")

	// Allow goroutines to settle
	time.Sleep(50 * time.Millisecond)
}

// TestManager_Start_OnlyInitializedServicesStarted verifies that only
// services that were initialized during Init are started.
func TestManager_Start_OnlyInitializedServicesStarted(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{Mode: ModeDistributed})

	// Only set triggerService, leave others nil
	mockTrigger := new(MockTriggerService)
	mockTrigger.On("Start", mock.Anything).Return(nil)
	mgr.triggerService = mockTrigger

	bgCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr.Start(bgCtx)

	// Wait for Start to be called
	time.Sleep(100 * time.Millisecond)

	// Only triggerService should have been started
	mockTrigger.AssertCalled(t, "Start", mock.Anything)

	// Other services are nil and should not cause panic
}
