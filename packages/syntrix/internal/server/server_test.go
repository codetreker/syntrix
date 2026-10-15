package server

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestNew(t *testing.T) {
	cfg := Config{
		Host:     "localhost",
		HTTPPort: 8080,
		GRPCPort: 9090,
	}
	srv := New(cfg, nil)
	require.NotNil(t, srv)
}

func TestServer_StartStop(t *testing.T) {
	// Use random ports to avoid conflicts
	cfg := Config{
		Host:     "localhost",
		HTTPPort: 0, // Let OS choose
		GRPCPort: 0, // Let OS choose
	}
	srv := New(cfg, nil)
	require.NotNil(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- srv.Start(ctx)
	}()

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	// Stop the server
	err := srv.Stop(context.Background())
	assert.NoError(t, err)

	cancel() // Signal Start to exit

	// Wait for Start to return
	select {
	case err := <-errChan:
		assert.NoError(t, err) // Should be nil on normal shutdown
	case <-time.After(1 * time.Second):
		t.Fatal("server did not stop in time")
	}
}

func TestServer_StartAfterCanceledShutdownDoesNotLaunchListeners(t *testing.T) {
	srv := New(Config{Host: "127.0.0.1", HTTPPort: 0, GRPCPort: 0}, nil).(*serverImpl)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, srv.Stop(ctx))
	})
	ownerCtx, ownerCancel := context.WithCancel(context.Background())
	ownerCancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	require.NoError(t, srv.Stop(shutdownCtx))

	// Startup can be scheduled after its owner has canceled and closed the server.
	require.NoError(t, srv.Start(ownerCtx))
	require.False(t, srv.started)
	require.Nil(t, srv.httpServer)
}

func TestServer_RegisterHTTP(t *testing.T) {
	cfg := Config{
		Host:     "localhost",
		HTTPPort: 0,
	}
	srv := New(cfg, nil)

	srv.RegisterHTTPHandler("/test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// We can't easily test if it's registered without starting,
	// but we can ensure it doesn't panic.
}

func TestServer_Start_AlreadyStarted(t *testing.T) {
	cfg := Config{Host: "localhost", HTTPPort: 0, GRPCPort: 0}
	srv := New(cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go srv.Start(ctx)
	time.Sleep(100 * time.Millisecond)

	err := srv.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "server already started")
}

func TestServer_Start_PortConflict(t *testing.T) {
	// Start a listener to occupy a port
	l, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	defer l.Close()

	port := l.Addr().(*net.TCPAddr).Port

	cfg := Config{
		Host:     "localhost",
		HTTPPort: port, // Conflict
		GRPCPort: 0,
	}
	srv := New(cfg, nil)

	// Should fail immediately or shortly after
	err = srv.Start(context.Background())
	assert.Error(t, err)
}

func TestServer_Start_GRPC_PortConflict(t *testing.T) {
	// Start a listener to occupy a port
	l, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	defer l.Close()

	port := l.Addr().(*net.TCPAddr).Port

	cfg := Config{
		Host:     "localhost",
		HTTPPort: 0,
		GRPCPort: port, // Conflict
	}
	srv := New(cfg, nil)

	// Should fail immediately or shortly after
	err = srv.Start(context.Background())
	assert.Error(t, err)
}

func TestServer_HTTPMux(t *testing.T) {
	cfg := Config{
		Host:     "localhost",
		HTTPPort: 0,
	}
	srv := New(cfg, nil).(*serverImpl)
	require.NotNil(t, srv)

	mux := srv.HTTPMux()
	require.NotNil(t, mux)
	assert.Equal(t, srv.httpMux, mux)
}

type blockingHealthServer struct {
	healthpb.UnimplementedHealthServer
	entered  chan struct{}
	finished chan error
}

func (s *blockingHealthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	close(s.entered)
	<-ctx.Done()
	s.finished <- ctx.Err()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func TestServer_Stop_ContextTimeout(t *testing.T) {
	srv := New(Config{GRPCMaxConcurrent: 1}, nil).(*serverImpl)
	t.Cleanup(srv.grpcServer.Stop)

	health := &blockingHealthServer{
		entered:  make(chan struct{}),
		finished: make(chan error, 1),
	}
	srv.grpcServer = grpc.NewServer()
	healthpb.RegisterHealthServer(srv.grpcServer, health)
	listener := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { _ = listener.Close() })
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.grpcServer.Serve(listener) }()

	conn, err := grpc.NewClient("passthrough:///shutdown-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	callCtx, cancelCall := context.WithCancel(context.Background())
	t.Cleanup(cancelCall)
	callDone := make(chan error, 1)
	go func() {
		_, err := healthpb.NewHealthClient(conn).Check(callCtx, &healthpb.HealthCheckRequest{})
		callDone <- err
	}()

	watchdog, cancelWatchdog := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWatchdog()
	select {
	case <-health.entered:
	case <-watchdog.Done():
		t.Fatal("RPC handler did not start")
	}

	// An active RPC keeps graceful shutdown pending until forced shutdown cancels it.
	stopCtx, cancelStop := context.WithCancel(context.Background())
	cancelStop()
	stopDone := make(chan error, 1)
	go func() { stopDone <- srv.Stop(stopCtx) }()
	select {
	case err := <-stopDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-watchdog.Done():
		t.Fatal("Stop did not interrupt the active RPC")
	}
	select {
	case err := <-health.finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-watchdog.Done():
		t.Fatal("RPC handler was not canceled")
	}
	select {
	case err := <-callDone:
		require.Equal(t, codes.Unavailable, status.Code(err))
	case <-watchdog.Done():
		t.Fatal("RPC client did not return")
	}
	select {
	case err := <-serveDone:
		require.NoError(t, err)
	case <-watchdog.Done():
		t.Fatal("gRPC server did not stop")
	}
}

func TestServer_AuthRateLimiter(t *testing.T) {
	t.Run("Returns nil when no auth rate limiter configured", func(t *testing.T) {
		cfg := Config{
			Host:     "localhost",
			HTTPPort: 0,
			GRPCPort: 0,
		}
		srv := New(cfg, nil).(*serverImpl)
		require.NotNil(t, srv)

		limiter := srv.AuthRateLimiter()
		assert.Nil(t, limiter)
	})

	t.Run("Returns auth rate limiter when rate limiting is enabled", func(t *testing.T) {
		cfg := Config{
			Host:     "localhost",
			HTTPPort: 0,
			GRPCPort: 0,
			RateLimit: RateLimitConfig{
				Enabled:      true,
				Requests:     100,
				Window:       time.Minute,
				AuthRequests: 5,
				AuthWindow:   time.Minute,
			},
		}
		srv := New(cfg, nil).(*serverImpl)
		require.NotNil(t, srv)

		limiter := srv.AuthRateLimiter()
		assert.NotNil(t, limiter)
	})
}

func TestStandardHealthStartsUnreadyAndPublishesRoleStatus(t *testing.T) {
	service := New(DefaultConfig(), nil).(*serverImpl)
	ctx := context.Background()
	observed, err := service.healthServer.Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, observed.Status)
	observed, err = service.healthServer.Check(ctx, &healthpb.HealthCheckRequest{Service: "syntrix.liveness"})
	require.NoError(t, err)
	require.Equal(t, healthpb.HealthCheckResponse_SERVING, observed.Status)
	for _, ready := range []bool{true, false} {
		service.SetServingStatus("syntrix.puller", ready)
		observed, err = service.healthServer.Check(ctx, &healthpb.HealthCheckRequest{Service: "syntrix.puller"})
		require.NoError(t, err)
		expected := healthpb.HealthCheckResponse_NOT_SERVING
		if ready {
			expected = healthpb.HealthCheckResponse_SERVING
		}
		require.Equal(t, expected, observed.Status)
	}
	require.NoError(t, service.Stop(ctx))
}

type heldHealthServer struct {
	healthpb.UnimplementedHealthServer
	entered, cancelled chan struct{}
	release            <-chan struct{}
}

func (s *heldHealthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	close(s.entered)
	<-ctx.Done()
	close(s.cancelled)
	<-s.release
	return nil, status.FromContextError(ctx.Err()).Err()
}

func TestForcedStopRetainsHandlerSettlementAcrossRetries(t *testing.T) {
	srv := New(DefaultConfig(), nil).(*serverImpl)
	srv.grpcServer = grpc.NewServer()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, srv.Stop(ctx))
	})
	handler := &heldHealthServer{entered: make(chan struct{}), cancelled: make(chan struct{}), release: release}
	healthpb.RegisterHealthServer(srv.grpcServer, handler)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.grpcServer.Serve(listener) }()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	request, requestCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer requestCancel()
	callDone := make(chan error, 1)
	go func() {
		_, err := healthpb.NewHealthClient(conn).Check(request, &healthpb.HealthCheckRequest{})
		callDone <- err
	}()
	select {
	case <-handler.entered:
	case <-request.Done():
		t.Fatal("RPC handler never started")
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stopCancel()
	firstResult := make(chan error, 1)
	go func() { firstResult <- srv.Stop(stopCtx) }()
	select {
	case err := <-firstResult:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		unblock()
		t.Fatal("forced stop exceeded its deadline")
	}
	graceful, forced := srv.grpcStopDone, srv.grpcForceDone
	require.NotNil(t, graceful)
	require.NotNil(t, forced)
	select {
	case <-graceful:
		t.Fatal("held RPC was falsely reported as settled")
	default:
	}
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	require.ErrorIs(t, srv.Stop(retryCtx), context.DeadlineExceeded)
	retryCancel()
	require.Equal(t, graceful, srv.grpcStopDone)
	require.Equal(t, forced, srv.grpcForceDone)
	unblock()
	settle, settleCancel := context.WithTimeout(context.Background(), time.Second)
	defer settleCancel()
	require.NoError(t, srv.Stop(settle))
	select {
	case <-callDone:
	case <-settle.Done():
		t.Fatal("forced RPC did not finish")
	}
}

func TestShutdownRequiresForcedSDKStopSettlementAfterGracefulCompletion(t *testing.T) {
	srv := New(DefaultConfig(), nil).(*serverImpl)
	graceful := make(chan struct{})
	close(graceful)
	forced := make(chan struct{})
	var forcedOnce sync.Once
	settleForced := func() { forcedOnce.Do(func() { close(forced) }) }
	srv.grpcStopDone = graceful
	srv.grpcForceDone = forced
	t.Cleanup(func() { settleForced(); srv.grpcServer.Stop() })
	timeout, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := srv.Stop(timeout)
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "grpc forced stop did not settle")
	retryCtx, retryCancel := context.WithTimeout(context.Background(), time.Second)
	defer retryCancel()
	result := make(chan error, 1)
	go func() { result <- srv.Stop(retryCtx) }()
	select {
	case err := <-result:
		t.Fatalf("shutdown returned before forced stop settled: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	settleForced()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-retryCtx.Done():
		t.Fatal("shutdown retry did not observe settlement")
	}
	require.Equal(t, graceful, srv.grpcStopDone)
	require.Equal(t, forced, srv.grpcForceDone)
	require.NoError(t, srv.Stop(context.Background()))
}
