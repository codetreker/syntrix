package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
	api_config "github.com/syntrixbase/syntrix/internal/gateway/config"
	"github.com/syntrixbase/syntrix/internal/gateway/realtime"
	"github.com/syntrixbase/syntrix/internal/query"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// Mocks
type MockQueryService struct {
	mock.Mock
	query.Service
}

func (m *MockQueryService) GetDocument(ctx context.Context, database string, path string) (model.Document, error) {
	args := m.Called(ctx, database, path)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

type MockAuthService struct {
	mock.Mock
	identity.AuthN
}

func (m *MockAuthService) Middleware(next http.Handler) http.Handler {
	// Database is now extracted from URL path, not context
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func (m *MockAuthService) MiddlewareOptional(next http.Handler) http.Handler {
	// Database is now extracted from URL path, not context
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

type MockAuthzEngine struct {
	mock.Mock
	identity.AuthZ
}

func (m *MockAuthzEngine) Evaluate(ctx context.Context, database string, path string, action string, req identity.AuthzRequest, existingRes *identity.Resource) (bool, error) {
	args := m.Called(ctx, database, path, action, req, existingRes)
	return args.Bool(0), args.Error(1)
}

func TestNewServer(t *testing.T) {
	mockQuery := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzEngine)

	server, err := NewServer(mockQuery, mockAuth, mockAuthz, nil)
	assert.NoError(t, err)
	assert.NotNil(t, server)
	assert.NotNil(t, server.rest)
}

func TestNewServer_WithRealtime(t *testing.T) {
	mockQuery := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzEngine)

	rt := realtime.NewServer(mockQuery, nil, "docs", mockAuth, api_config.RealtimeConfig{})

	server, err := NewServer(mockQuery, mockAuth, mockAuthz, rt)
	assert.NoError(t, err)
	assert.NotNil(t, server)
	assert.NotNil(t, server.realtime)
}

func TestServer_RegisterRoutes(t *testing.T) {
	mockQuery := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzEngine)

	server, err := NewServer(mockQuery, mockAuth, mockAuthz, nil)
	assert.NoError(t, err)

	// Create a mux and register routes
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	t.Run("Routes registered - health endpoint", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()

		mux.ServeHTTP(w, req)

		// Health endpoint should respond (may be 200 or error depending on engine state)
		// The key is that the route is registered and handled
		assert.NotEqual(t, http.StatusNotFound, w.Code)
	})

	t.Run("Routes registered - API endpoint", func(t *testing.T) {
		// Mock GetDocument to return ErrNotFound
		// Use new URL format: /api/v1/databases/{database}/documents/{path...}
		mockQuery.On("GetDocument", mock.Anything, "default", "users/test").Return(nil, model.ErrNotFound)
		mockAuthz.On("Evaluate", mock.Anything, "default", "users/test", "read", mock.Anything, mock.Anything).Return(true, nil)

		req := httptest.NewRequest("GET", "/api/v1/databases/default/documents/users/test", nil)
		w := httptest.NewRecorder()

		mux.ServeHTTP(w, req)

		// Route should be registered (404 from handler, not from mux)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestServer_RegisterRoutes_WithRealtime(t *testing.T) {
	mockQuery := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzEngine)

	rt := realtime.NewServer(mockQuery, nil, "docs", mockAuth, api_config.RealtimeConfig{})
	server, err := NewServer(mockQuery, mockAuth, mockAuthz, rt)
	assert.NoError(t, err)

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	t.Run("Realtime WS route registered", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/realtime/ws", nil)
		w := httptest.NewRecorder()

		mux.ServeHTTP(w, req)

		// Should not be 404 (route exists, may fail for other reasons like missing upgrade header)
		assert.NotEqual(t, http.StatusNotFound, w.Code)
	})

	t.Run("Realtime SSE route registered", func(t *testing.T) {
		// For SSE, we can't easily test the full flow because it requires Hub.Run().
		// Instead, we verify the route exists by checking that the response is not 404.
		// We use a mock approach: call with invalid method to confirm route exists.
		req := httptest.NewRequest("POST", "/realtime/sse", nil) // SSE only accepts GET
		w := httptest.NewRecorder()

		mux.ServeHTTP(w, req)

		// 405 Method Not Allowed means route exists but method is wrong
		// This confirms the route is registered without triggering the blocking SSE handler
		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})
}

func TestServer_SetDatabaseService(t *testing.T) {
	mockQuery := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzEngine)

	server, err := NewServer(mockQuery, mockAuth, mockAuthz, nil)
	assert.NoError(t, err)
	assert.NotNil(t, server)

	// SetDatabaseService should not panic and should set the service
	// We can't easily mock database.Service without implementing all methods,
	// so we test that nil is accepted (which it is, as a no-op)
	server.SetDatabaseService(nil)

	// The function should have completed without panic
	// This is a smoke test to ensure the method works
}

func TestNewServer_WithOptions(t *testing.T) {
	mockQuery := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzEngine)

	t.Run("WithDatabase option", func(t *testing.T) {
		mockDB := &stubDatabaseService{}
		server, err := NewServer(mockQuery, mockAuth, mockAuthz, nil,
			WithDatabase(mockDB))

		assert.NoError(t, err)
		assert.NotNil(t, server)
	})

	t.Run("WithServerAuthRateLimiter option", func(t *testing.T) {
		limiter := &stubRateLimiter{allowResult: true}
		server, err := NewServer(mockQuery, mockAuth, mockAuthz, nil,
			WithServerAuthRateLimiter(limiter, 60000000000))

		assert.NoError(t, err)
		assert.NotNil(t, server)
	})

	t.Run("Multiple options", func(t *testing.T) {
		mockDB := &stubDatabaseService{}
		limiter := &stubRateLimiter{allowResult: true}
		server, err := NewServer(mockQuery, mockAuth, mockAuthz, nil,
			WithDatabase(mockDB),
			WithServerAuthRateLimiter(limiter, 60000000000))

		assert.NoError(t, err)
		assert.NotNil(t, server)
	})
}

func TestServer_SetAuthRateLimiter(t *testing.T) {
	mockQuery := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzEngine)

	server, err := NewServer(mockQuery, mockAuth, mockAuthz, nil)
	assert.NoError(t, err)

	limiter := &stubRateLimiter{allowResult: true}
	// Should not panic
	server.SetAuthRateLimiter(limiter, 60000000000)
}

func TestServer_RegisterRoutes_Console(t *testing.T) {
	// Create temp dir with console/dist structure
	tmpDir := t.TempDir()
	distDir := filepath.Join(tmpDir, "console", "dist")
	assetsDir := filepath.Join(distDir, "assets")
	assert.NoError(t, os.MkdirAll(assetsDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(distDir, "index.html"), []byte(`<!DOCTYPE html><html><body>SPA</body></html>`), 0o644))
	assert.NoError(t, os.WriteFile(filepath.Join(assetsDir, "app.js"), []byte(`console.log("app")`), 0o644))

	// Chdir to temp dir so the handler can find console/dist
	oldDir, err := os.Getwd()
	assert.NoError(t, err)
	assert.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { os.Chdir(oldDir) })

	mockQuery := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzEngine)

	server, err := NewServer(mockQuery, mockAuth, mockAuthz, nil)
	assert.NoError(t, err)

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	t.Run("serves index.html at root", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/console/", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		body, _ := io.ReadAll(w.Result().Body)
		assert.Contains(t, string(body), "SPA")
	})

	t.Run("serves real static file", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/console/assets/app.js", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		body, _ := io.ReadAll(w.Result().Body)
		assert.Contains(t, string(body), `console.log("app")`)
	})

	t.Run("SPA fallback for non-existent path", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/console/databases", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		body, _ := io.ReadAll(w.Result().Body)
		assert.Contains(t, string(body), "SPA")
	})

	t.Run("SPA fallback for deep non-existent path", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/console/nonexistent/deep/path", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		body, _ := io.ReadAll(w.Result().Body)
		assert.Contains(t, string(body), "SPA")
	})
}

// stubDatabaseService is a minimal stub implementing database.Service
type stubDatabaseService struct{}

func (s *stubDatabaseService) CreateDatabase(ctx context.Context, userID string, isAdmin bool, req database.CreateRequest) (*database.Database, error) {
	return nil, nil
}
func (s *stubDatabaseService) ListDatabases(ctx context.Context, userID string, isAdmin bool, opts database.ListOptions) (*database.ListResult, error) {
	return nil, nil
}
func (s *stubDatabaseService) GetDatabase(ctx context.Context, userID string, isAdmin bool, identifier string) (*database.Database, error) {
	return nil, nil
}
func (s *stubDatabaseService) UpdateDatabase(ctx context.Context, userID string, isAdmin bool, identifier string, req database.UpdateRequest) (*database.Database, error) {
	return nil, nil
}
func (s *stubDatabaseService) DeleteDatabase(ctx context.Context, userID string, isAdmin bool, identifier string) error {
	return nil
}
func (s *stubDatabaseService) ResolveDatabase(ctx context.Context, identifier string) (*database.Database, error) {
	return nil, nil
}
func (s *stubDatabaseService) ValidateDatabase(ctx context.Context, identifier string) error {
	return nil
}

// stubRateLimiter is a minimal stub implementing ratelimit.Limiter
type stubRateLimiter struct {
	allowResult bool
}

func (s *stubRateLimiter) Allow(key string) bool {
	return s.allowResult
}

func (s *stubRateLimiter) Reset(key string) {
	// no-op
}
