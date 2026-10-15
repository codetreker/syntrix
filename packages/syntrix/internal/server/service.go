package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/codetreker/syntrix/internal/query/wire"
	"github.com/codetreker/syntrix/internal/server/ratelimit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

type serverImpl struct {
	cfg    Config
	logger *slog.Logger

	// HTTP State
	httpMux    *http.ServeMux
	httpServer *http.Server

	// Rate Limiting
	rateLimiter     ratelimit.Limiter // General rate limiter
	authRateLimiter ratelimit.Limiter // Stricter auth-specific rate limiter

	// gRPC State
	grpcServer    *grpc.Server
	grpcStopDone  chan struct{}
	grpcForceDone chan struct{}
	healthServer  *health.Server
	listenerWG    sync.WaitGroup
	stopped       bool

	// Lifecycle State
	mu      sync.Mutex
	started bool
}

// New creates a new Service instance.
func New(cfg Config, logger *slog.Logger) Service {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "server")

	s := &serverImpl{
		cfg:     cfg,
		logger:  logger,
		httpMux: http.NewServeMux(),
	}

	// Initialize rate limiters if enabled
	if cfg.RateLimit.Enabled {
		s.rateLimiter = ratelimit.NewMemoryLimiter(ratelimit.Config{
			Enabled:  cfg.RateLimit.Enabled,
			Requests: cfg.RateLimit.Requests,
			Window:   cfg.RateLimit.Window,
		})
		// Stricter rate limiter for auth endpoints
		authWindow := cfg.RateLimit.AuthWindow
		if authWindow == 0 {
			authWindow = cfg.RateLimit.Window
		}
		s.authRateLimiter = ratelimit.NewMemoryLimiter(ratelimit.Config{
			Enabled:  cfg.RateLimit.Enabled,
			Requests: cfg.RateLimit.AuthRequests,
			Window:   authWindow,
		})
	}

	// Initialize gRPC server immediately to allow registration
	opts := []grpc.ServerOption{
		grpc.MaxConcurrentStreams(uint32(s.cfg.GRPCMaxConcurrent)),
		grpc.MaxRecvMsgSize(wire.MaxGRPCBytes),
		s.unaryInterceptors(),
		s.streamInterceptors(),
	}
	s.grpcServer = grpc.NewServer(opts...)
	s.healthServer = health.NewServer()
	s.healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	s.healthServer.SetServingStatus("syntrix.liveness", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s.grpcServer, s.healthServer)

	if s.cfg.EnableReflection {
		reflection.Register(s.grpcServer)
	}

	return s
}

func (s *serverImpl) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("server already started")
	}
	if s.stopped || ctx.Err() != nil {
		s.mu.Unlock()
		return nil
	}
	s.initHTTPServer()
	httpListener, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("http listen error: %w", err)
	}
	grpcListener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.GRPCPort))
	if err != nil {
		_ = httpListener.Close()
		s.mu.Unlock()
		return fmt.Errorf("grpc listen error: %w", err)
	}
	s.started = true
	errChan := make(chan error, 2)
	s.listenerWG.Add(2)
	go func() {
		defer s.listenerWG.Done()
		if err := s.httpServer.Serve(httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- fmt.Errorf("http server error: %w", err)
		}
	}()
	go func() {
		defer s.listenerWG.Done()
		if err := s.grpcServer.Serve(grpcListener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errChan <- fmt.Errorf("grpc server error: %w", err)
		}
	}()
	s.mu.Unlock()
	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return nil
	}
}

func (s *serverImpl) SetServingStatus(role string, ready bool) {
	status := healthpb.HealthCheckResponse_NOT_SERVING
	if ready {
		status = healthpb.HealthCheckResponse_SERVING
	}
	s.healthServer.SetServingStatus(role, status)
}

func (s *serverImpl) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.healthServer.Shutdown()

	var wg sync.WaitGroup
	errChan := make(chan error, 2)

	// Shutdown HTTP
	if s.httpServer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.logger.Info("Stopping HTTP server")
			if err := s.httpServer.Shutdown(ctx); err != nil {
				errChan <- fmt.Errorf("http shutdown error: %w", err)
			}
		}()
	}

	// Shutdown gRPC
	if s.grpcServer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.logger.Info("Stopping gRPC server")

			if s.grpcStopDone == nil {
				s.grpcStopDone = make(chan struct{})
				go func() { s.grpcServer.GracefulStop(); close(s.grpcStopDone) }()
			}
			select {
			case <-s.grpcStopDone:
				if s.grpcForceDone != nil {
					select {
					case <-s.grpcForceDone:
					case <-ctx.Done():
						errChan <- fmt.Errorf("grpc forced stop did not settle before its deadline: %w", ctx.Err())
					}
				}
			case <-ctx.Done():
				s.logger.Warn("Context deadline exceeded, forcing gRPC stop")
				if s.grpcForceDone == nil {
					s.grpcForceDone = make(chan struct{})
					go func() { s.grpcServer.Stop(); close(s.grpcForceDone) }()
				}
				errChan <- fmt.Errorf("grpc shutdown did not settle before its deadline: %w", ctx.Err())
			}
		}()
	}

	wg.Wait()
	s.listenerWG.Wait()
	close(errChan)

	// Stop rate limiter cleanup goroutines
	if s.rateLimiter != nil {
		if stoppable, ok := s.rateLimiter.(ratelimit.Stoppable); ok {
			stoppable.Stop()
		}
	}
	if s.authRateLimiter != nil {
		if stoppable, ok := s.authRateLimiter.(ratelimit.Stoppable); ok {
			stoppable.Stop()
		}
	}

	var errs []error
	for err := range errChan {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (s *serverImpl) RegisterHTTPHandler(pattern string, handler http.Handler) {
	s.httpMux.Handle(pattern, handler)
}

func (s *serverImpl) RegisterGRPCService(desc *grpc.ServiceDesc, impl interface{}) {
	s.grpcServer.RegisterService(desc, impl)
}

func (s *serverImpl) HTTPMux() *http.ServeMux {
	return s.httpMux
}

func (s *serverImpl) AuthRateLimiter() ratelimit.Limiter {
	return s.authRateLimiter
}
