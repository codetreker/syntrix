package gateway

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/codetreker/syntrix/internal/core/database"
	"github.com/codetreker/syntrix/internal/gateway/authorization"
	"github.com/codetreker/syntrix/internal/gateway/realtime"
	"github.com/codetreker/syntrix/internal/gateway/rest"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/query"
	"github.com/codetreker/syntrix/internal/server/ratelimit"
)

// Server is a route registrar for the API layer.
// It registers REST, realtime, and console routes to a given ServeMux.
type Server struct {
	rest     *rest.Handler
	realtime *realtime.Server
}

// ServerOption is a function that configures a Server.
type ServerOption func(*serverConfig)

type serverConfig struct {
	dbService       database.Service
	authRateLimiter ratelimit.Limiter
	authRLWindow    time.Duration
}

// WithDatabase configures the server with a database service.
func WithDatabase(svc database.Service) ServerOption {
	return func(c *serverConfig) {
		c.dbService = svc
	}
}

// WithAuthRateLimiter configures the server with a stricter rate limiter for auth endpoints.
func WithServerAuthRateLimiter(limiter ratelimit.Limiter, window time.Duration) ServerOption {
	return func(c *serverConfig) {
		c.authRateLimiter = limiter
		c.authRLWindow = window
	}
}

// NewServer creates a new API Server (route registrar).
func NewServer(engine query.Service, auth identity.AccountService, verifier identity.TokenVerifier, authz authorization.Engine, rt *realtime.Server, opts ...ServerOption) (*Server, error) {
	// Collect server options
	cfg := &serverConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// Build rest handler options
	var restOpts []rest.HandlerOption
	if cfg.dbService != nil {
		restOpts = append(restOpts, rest.WithDatabaseService(cfg.dbService))
		if rt != nil {
			rt.SetDatabaseService(cfg.dbService)
		}
	}
	if cfg.authRateLimiter != nil {
		restOpts = append(restOpts, rest.WithAuthRateLimiter(cfg.authRateLimiter, cfg.authRLWindow))
	}

	restHandler, err := rest.NewHandler(engine, auth, verifier, authz, restOpts...)
	if err != nil {
		return nil, err
	}
	return &Server{
		rest:     restHandler,
		realtime: rt,
	}, nil
}

// RegisterRoutes registers all API routes to the given ServeMux.
// This includes REST API routes, realtime WebSocket/SSE routes, and console static files.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// Register REST routes
	s.rest.RegisterRoutes(mux)

	// Register Realtime routes
	if s.realtime != nil {
		mux.HandleFunc("GET /realtime/ws", s.realtime.HandleWS)
		mux.HandleFunc("GET /realtime/sse", s.realtime.HandleSSE)
	}

	// Console – serve built frontend with SPA fallback
	consoleDir := "../console/dist"
	mux.HandleFunc("GET /console/", func(w http.ResponseWriter, r *http.Request) {
		// Strip the /console/ prefix to get the relative file path
		relPath := r.URL.Path[len("/console/"):]
		// Check if the requested file exists on disk
		if relPath != "" {
			absPath := filepath.Join(consoleDir, filepath.Clean("/"+relPath))
			if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
				http.StripPrefix("/console/", http.FileServer(http.Dir(consoleDir))).ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback: serve index.html for client-side routing
		http.ServeFile(w, r, filepath.Join(consoleDir, "index.html"))
	})
}

// SetDatabaseService injects the database service for database management operations.
// Deprecated: Use WithDatabase option in NewServer instead.
// This method is kept for backward compatibility.
func (s *Server) SetDatabaseService(svc database.Service) {
	s.rest.SetDatabaseService(svc)
	if s.realtime != nil {
		s.realtime.SetDatabaseService(svc)
	}
}

// SetAuthRateLimiter sets the stricter rate limiter for auth endpoints.
// Deprecated: Use WithServerAuthRateLimiter option in NewServer instead.
// This method is kept for backward compatibility.
func (s *Server) SetAuthRateLimiter(limiter ratelimit.Limiter, window time.Duration) {
	s.rest.SetAuthRateLimiter(limiter, window)
}
