package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/codetreker/syntrix/internal/gateway"
	gatewayconfig "github.com/codetreker/syntrix/internal/gateway/config"
	"github.com/codetreker/syntrix/internal/gateway/realtime"
	"github.com/codetreker/syntrix/internal/indexer"
	"github.com/codetreker/syntrix/internal/initialization"
	initializationconfig "github.com/codetreker/syntrix/internal/initialization/config"
	"github.com/codetreker/syntrix/internal/server"
	"github.com/codetreker/syntrix/internal/streamer"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type readyPuller struct {
	stubPullerService
	check    func(context.Context) error
	terminal error
}

func (s *readyPuller) Readiness(ctx context.Context) error { return s.check(ctx) }
func (s *readyPuller) TerminalError() error                { return s.terminal }

type readyIndexer struct {
	indexer.LocalService
	status        indexer.HealthStatus
	err, terminal error
}

func (s *readyIndexer) Health(context.Context) (indexer.Health, error) {
	return indexer.Health{Status: s.status}, s.err
}
func (s *readyIndexer) TerminalError() error     { return s.terminal }
func (*readyIndexer) Stop(context.Context) error { return nil }

type readyTask struct {
	check    func(context.Context) error
	terminal error
}

func (*readyTask) Start(ctx context.Context) error       { <-ctx.Done(); return nil }
func (s *readyTask) Readiness(ctx context.Context) error { return s.check(ctx) }
func (s *readyTask) TerminalError() error                { return s.terminal }

type readyBroker struct {
	pubsub.Provider
	err error
}

func (s *readyBroker) Check(context.Context) error { return s.err }
func (*readyBroker) Close() error                  { return nil }

type observedInitialization struct {
	fakeInitialization
	state string
	err   error
}

func (s observedInitialization) Inspect(context.Context) (initialization.Status, error) {
	return initialization.Status{State: s.state}, s.err
}

type readyStreamer struct{ check func(context.Context) error }

func (*readyStreamer) Start(context.Context) error           { return nil }
func (*readyStreamer) Stop(context.Context) error            { return nil }
func (s *readyStreamer) Readiness(ctx context.Context) error { return s.check(ctx) }
func (*readyStreamer) Stream(ctx context.Context) (streamer.Stream, error) {
	owner, cancel := context.WithCancel(ctx)
	return &readyStream{ctx: owner, cancel: cancel}, nil
}

type readyStream struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (*readyStream) Subscribe(context.Context, string, string, []model.Filter) (streamer.Registration, error) {
	return streamer.Registration{}, nil
}
func (s *readyStream) Status() streamer.StreamStatus {
	return streamer.StreamStatus{State: streamer.StateConnected, Generation: 1}
}
func (*readyStream) Unsubscribe(string) error { return nil }
func (s *readyStream) Recv() (*streamer.EventDelivery, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}
func (s *readyStream) Close() error { s.cancel(); return nil }

func readinessManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	previous := server.Default()
	server.InitDefault(server.DefaultConfig(), nil)
	t.Cleanup(func() { server.SetDefault(previous) })
	cfg := config.DefaultConfig()
	cfg.Services.Readiness.Interval = 10 * time.Millisecond
	cfg.Services.Readiness.Timeout = 20 * time.Millisecond
	mgr := NewManager(cfg, opts)
	mgr.initHealth()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, mgr.shutdown(ctx))
	})
	return mgr
}

func TestRuntimeProbesUseFreshSnapshotsAndKeepHeldRolesUnready(t *testing.T) {
	mgr := readinessManager(t, Options{RunPuller: true, RunTriggerEvaluator: true, RunTriggerWorker: true, BootstrapIndexes: true})
	require.False(t, mgr.health.active[roleEvaluator])
	require.False(t, mgr.health.active[roleWorker])
	mgr.health.active[roleIndexer] = false
	var unavailable atomic.Bool
	mgr.pullerService = &readyPuller{check: func(context.Context) error {
		if unavailable.Load() {
			return errors.New("fixture-private-password")
		}
		return nil
	}}
	probe := func(method, path string, expected int) {
		t.Helper()
		result := httptest.NewRecorder()
		server.Default().HTTPMux().ServeHTTP(result, httptest.NewRequest(method, path, nil))
		require.Equal(t, expected, result.Code)
		require.NotContains(t, result.Body.String(), "fixture-private-password")
		if method == http.MethodHead {
			require.Empty(t, result.Body.String())
		}
	}
	probe(http.MethodGet, "/ready", http.StatusServiceUnavailable)
	mgr.checkReadiness(context.Background())
	probe(http.MethodGet, "/ready", http.StatusOK)
	probe(http.MethodHead, "/ready", http.StatusOK)
	time.Sleep(mgr.health.freshness + 5*time.Millisecond)
	probe(http.MethodGet, "/ready", http.StatusServiceUnavailable)
	unavailable.Store(true)
	mgr.checkReadiness(context.Background())
	probe(http.MethodGet, "/ready", http.StatusServiceUnavailable)
	unavailable.Store(false)
	mgr.checkReadiness(context.Background())
	probe(http.MethodGet, "/ready", http.StatusOK)
	probe(http.MethodPost, "/ready", http.StatusMethodNotAllowed)
	probe(http.MethodGet, "/ready/other", http.StatusNotFound)
	mgr.clearHealth()
	probe(http.MethodGet, "/ready", http.StatusServiceUnavailable)
}

func TestReadinessHasOneOwnedCheckPerRoleAndRetainsResourcesUntilDrain(t *testing.T) {
	mgr := readinessManager(t, Options{RunPuller: true})
	release := make(chan struct{})
	var calls atomic.Int32
	mgr.pullerService = &readyPuller{check: func(context.Context) error { calls.Add(1); <-release; return nil }}
	mgr.checkReadiness(context.Background())
	for attempt := 0; attempt < 8; attempt++ {
		mgr.checkReadiness(context.Background())
	}
	require.Equal(t, int32(1), calls.Load())
	require.False(t, mgr.health.ready())
	timeout, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	require.ErrorIs(t, mgr.shutdown(timeout), context.DeadlineExceeded)
	cancel()
	require.NotNil(t, mgr.pullerService)
	close(release)
	require.Eventually(t, func() bool {
		mgr.health.mu.RLock()
		defer mgr.health.mu.RUnlock()
		return !mgr.health.inFlight[rolePuller]
	}, time.Second, time.Millisecond)
	require.NoError(t, mgr.shutdown(context.Background()))
}

func TestRuntimeFatalFailurePreservesCauseAndSafeDiagnostics(t *testing.T) {
	mgr := readinessManager(t, Options{RunPuller: true})
	cause := errors.New("nats://secret-user:fixture-private-password@example.com:4222")
	mgr.lifecycle(context.Background())
	mgr.pullerService = &readyPuller{check: func(context.Context) error { return cause }, terminal: cause}
	mgr.checkReadiness(context.Background())
	result := mgr.Wait(context.Background())
	require.ErrorIs(t, result, cause)
	require.NotContains(t, result.Error(), "fixture-private-password")
	diagnostic := SafeError(mgr.cfg, rolePuller, "running", "terminal_failure", cause)
	require.ErrorIs(t, diagnostic, cause)
	var output bytes.Buffer
	slog.New(slog.NewJSONHandler(&output, nil)).Error("runtime failed", "failure", diagnostic)
	require.NotContains(t, output.String(), "fixture-private-password")
	for _, field := range []string{"subsystem", "role", "phase", "category", "instance_id", "allocation_id", "spec_revision"} {
		require.Contains(t, output.String(), field)
	}
	require.Error(t, mgr.checkSource(context.Background(), rolePuller, &stubPullerService{}))
}

func TestRoleReadinessDependenciesAndInitializationPhases(t *testing.T) {
	mgr := readinessManager(t, Options{Mode: ModeStandalone})
	ctx := context.Background()
	require.Error(t, mgr.storageReady(ctx))
	mgr.initialization = observedInitialization{state: initialization.Prepared}
	require.NoError(t, mgr.storageReady(ctx))
	mgr.cfg.Initialization.Mode = initializationconfig.Managed
	require.Error(t, mgr.storageReady(ctx))
	mgr.opts.InitializationAcceptance = true
	require.NoError(t, mgr.storageReady(ctx))
	mgr.opts.InitializationAcceptance = false
	mgr.initialization = observedInitialization{state: initialization.Finalized}
	pullerSource := &readyPuller{check: func(context.Context) error { return nil }}
	mgr.pullerService = pullerSource
	idx := &readyIndexer{status: indexer.HealthOK}
	mgr.indexerService = idx
	source := &readyStreamer{check: func(context.Context) error { return nil }}
	mgr.streamerService = source
	mgr.pubsubProvider = &readyBroker{}
	mgr.identityModule = &fakeIdentityModule{}
	task := &readyTask{check: func(context.Context) error { return nil }}
	mgr.triggerService = task
	mgr.triggerConsumer = task
	for _, role := range []string{rolePuller, roleIndexer, roleQuery, roleStreamer, roleEvaluator, roleWorker} {
		require.NoError(t, mgr.checkRole(ctx, role), role)
	}
	require.Error(t, mgr.checkRole(ctx, "unknown"))
	idx.status = indexer.HealthDegraded
	require.Error(t, mgr.checkRole(ctx, roleIndexer))
	idx.status = indexer.HealthOK
	idx.err = errors.New("store failure")
	require.Error(t, mgr.checkRole(ctx, roleIndexer))
	idx.err = nil
	mgr.initialization = observedInitialization{err: errors.New("storage failure")}
	for _, role := range []string{roleQuery, roleEvaluator, roleWorker} {
		require.Error(t, mgr.checkRole(ctx, role))
	}
	mgr.initialization = observedInitialization{state: initialization.Finalized}
	mgr.pubsubProvider = &readyBroker{err: errors.New("broker failure")}
	require.Error(t, mgr.checkRole(ctx, roleEvaluator))
	require.Error(t, mgr.checkRole(ctx, roleWorker))
	mgr.pubsubProvider = nil
	require.Error(t, mgr.brokerReady(ctx))
	mgr.pubsubProvider = &readyBroker{}
	mgr.identityModule = nil
	require.Error(t, mgr.checkRole(ctx, roleWorker))
	require.Error(t, mgr.checkRole(ctx, roleAPI))
	mgr.identityModule = &fakeIdentityModule{}
	mgr.gatewayServer = &gateway.Server{}
	mgr.rtServer = realtime.NewServer(&MockQueryService{}, source, "documents", &MockAuthService{}, gatewayconfig.RealtimeConfig{})
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	require.NoError(t, mgr.rtServer.StartBackgroundTasks(life))
	require.Eventually(t, func() bool { return mgr.checkRole(ctx, roleAPI) == nil }, time.Second, time.Millisecond)
	mgr.initialization = observedInitialization{err: errors.New("catalog unavailable")}
	require.Error(t, mgr.checkRole(ctx, roleAPI))
	mgr.initialization = observedInitialization{state: initialization.Finalized}
	idx.status = indexer.HealthUnhealthy
	require.Error(t, mgr.checkRole(ctx, roleAPI))
	idx.status = indexer.HealthOK
	source.check = func(context.Context) error { return errors.New("ingestion unavailable") }
	require.Error(t, mgr.checkRole(ctx, roleAPI))
	source.check = func(context.Context) error { return nil }
	task.check = func(context.Context) error { return errors.New("task unavailable") }
	require.Error(t, mgr.checkRole(ctx, roleEvaluator))
	require.Error(t, mgr.checkRole(ctx, roleWorker))
	task.check = func(context.Context) error { return nil }
	idx.terminal = errors.New("index storage failed")
	require.ErrorIs(t, mgr.checkRole(ctx, roleIndexer), idx.terminal)
	idx.terminal = nil
	mgr.indexerService = nil
	require.Error(t, mgr.checkRole(ctx, roleIndexer))
	mgr.indexerService = idx
	require.NoError(t, mgr.rtServer.Shutdown(ctx))
	require.Error(t, mgr.checkRole(ctx, roleAPI))
}

func TestRemoteReadinessUsesRequiredRoleInsteadOfAggregate(t *testing.T) {
	mgr := readinessManager(t, Options{Mode: ModeDistributed})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	remote := grpc.NewServer()
	status := health.NewServer()
	status.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	for _, role := range runtimeRoles {
		status.SetServingStatus(role, healthpb.HealthCheckResponse_SERVING)
	}
	healthpb.RegisterHealthServer(remote, status)
	go func() { _ = remote.Serve(listener) }()
	t.Cleanup(remote.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, mgr.remoteReady(ctx, listener.Addr().String(), rolePuller))
	require.Error(t, mgr.remoteReady(ctx, listener.Addr().String(), ""))
	require.Error(t, mgr.remoteReady(ctx, "", rolePuller))
	require.Error(t, mgr.remoteReady(ctx, "\x00", rolePuller))
	status.SetServingStatus(rolePuller, healthpb.HealthCheckResponse_NOT_SERVING)
	require.Error(t, mgr.remoteReady(ctx, listener.Addr().String(), rolePuller))
	status.SetServingStatus(rolePuller, healthpb.HealthCheckResponse_SERVING)
	mgr.initialization = observedInitialization{state: initialization.Finalized}
	mgr.indexerService = &readyIndexer{status: indexer.HealthOK}
	mgr.streamerService = &readyStreamer{check: func(context.Context) error { return nil }}
	mgr.cfg.Indexer.PullerAddr = listener.Addr().String()
	mgr.cfg.Query.IndexerAddr = listener.Addr().String()
	mgr.cfg.Streamer.Server.PullerAddr = listener.Addr().String()
	for _, role := range []string{roleIndexer, roleQuery, roleStreamer} {
		require.NoError(t, mgr.checkRole(ctx, role), fmt.Sprintf("role %s", role))
	}
	mgr.identityModule = &fakeIdentityModule{}
	mgr.gatewayServer = &gateway.Server{}
	source := &readyStreamer{check: func(context.Context) error { return nil }}
	mgr.rtServer = realtime.NewServer(&MockQueryService{}, source, "documents", &MockAuthService{}, gatewayconfig.RealtimeConfig{})
	require.NoError(t, mgr.rtServer.StartBackgroundTasks(ctx))
	mgr.cfg.Gateway.QueryServiceURL = listener.Addr().String()
	mgr.cfg.Streamer.Client.StreamerAddr = listener.Addr().String()
	mgr.cfg.Gateway.StreamerServiceURL = ""
	require.Eventually(t, func() bool { return mgr.checkRole(ctx, roleAPI) == nil }, time.Second, time.Millisecond)
	status.SetServingStatus(roleQuery, healthpb.HealthCheckResponse_NOT_SERVING)
	require.Error(t, mgr.checkRole(ctx, roleAPI))
	status.SetServingStatus(roleQuery, healthpb.HealthCheckResponse_SERVING)
	status.SetServingStatus(roleStreamer, healthpb.HealthCheckResponse_NOT_SERVING)
	require.Error(t, mgr.checkRole(ctx, roleAPI))
	status.SetServingStatus(roleStreamer, healthpb.HealthCheckResponse_SERVING)
	mgr.cfg.Gateway.StreamerServiceURL = listener.Addr().String()
	require.NoError(t, mgr.checkRole(ctx, roleAPI))
}
