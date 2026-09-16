package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/internal/server/ratelimit"
	"google.golang.org/grpc"
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
	grpcServer *grpc.Server

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
		// General rate limiter for all endpoints
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
	s.started = true

	// Initialize HTTP Server while holding the lock
	s.initHTTPServer()
	s.mu.Unlock()

	errChan := make(chan error, 2)

	// Start HTTP Listener
	go s.runHTTPServer(errChan)

	// Start gRPC Listener
	go s.runGRPCServer(errChan)

	// Wait for Error or Context Cancellation
	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		return nil // Normal shutdown signal
	}
}

func (s *serverImpl) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

			done := make(chan struct{})
			go func() {
				s.grpcServer.GracefulStop()
				close(done)
			}()

			select {
			case <-done:
				// Graceful stop completed
			case <-ctx.Done():
				s.logger.Warn("Context deadline exceeded, forcing gRPC stop")
				s.grpcServer.Stop()
			}
		}()
	}

	wg.Wait()
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
