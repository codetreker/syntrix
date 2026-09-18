package rest

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
)

// TestAuthorized_GetDocumentError covers the case where GetDocument returns an error other than ErrNotFound
func TestAuthorized_GetDocumentError(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzService)

	handler, _ := NewHandler(mockService, mockAuth, mockAuthz)

	// We need to wrap a dummy handler with authorized
	target := handler.authorized(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, "update")

	// Mock GetDocument to return an error
	mockService.On("GetDocument", mock.Anything, "default", "col/doc").Return(nil, errors.New("db error"))

	req, _ := http.NewRequest("PUT", "/api/v1/databases/default/documents/col/doc", nil)
	req.SetPathValue("database", "default")
	req.SetPathValue("path", "col/doc")

	// Add database to context so databaseOrError passes (though authorized doesn't call it directly,
	// it uses r.Context() for GetDocument which usually needs database, but here we mock it with "default")
	// Wait, authorized calls h.engine.GetDocument(r.Context(), "default", path) directly.
	// So we don't need database in context for this specific call in authorized.

	rr := httptest.NewRecorder()
	target(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), "Failed to check resource")
}

// MockAuthService_NoContext is a mock that calls next but doesn't set any context
type MockAuthService_NoContext struct {
	mock.Mock
}

func (m *MockAuthService_NoContext) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
func (m *MockAuthService_NoContext) MiddlewareOptional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
func (m *MockAuthService_NoContext) SignIn(ctx context.Context, req identity.LoginRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (m *MockAuthService_NoContext) SignUp(ctx context.Context, req identity.SignupRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (m *MockAuthService_NoContext) Refresh(ctx context.Context, req identity.RefreshRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (m *MockAuthService_NoContext) ListUsers(ctx context.Context, limit int, offset int) ([]*identity.User, error) {
	return nil, nil
}
func (m *MockAuthService_NoContext) UpdateUser(ctx context.Context, id string, roles []string, dbAdmin []string, disabled bool) error {
	return nil
}
func (m *MockAuthService_NoContext) Logout(ctx context.Context, refreshToken string) error {
	return nil
}
func (m *MockAuthService_NoContext) GenerateSystemToken(serviceName string) (string, error) {
	return "", nil
}
func (m *MockAuthService_NoContext) ValidateToken(tokenString string) (*identity.Claims, error) {
	return nil, nil
}

// TestTriggerProtected_NoRoles covers the case where ContextKeyRoles is missing
func TestTriggerProtected_NoRoles(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService_NoContext) // Use the no-context mock
	mockAuthz := new(AllowAllAuthzService)

	handler, _ := NewHandler(mockService, mockAuth, mockAuthz)

	// Wrap a dummy handler
	target := handler.triggerProtected(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("POST", "/trigger/v1/databases/default/write", nil)
	rr := httptest.NewRecorder()

	target(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "Access denied")
}

// TestAdminOnly_NoRoles covers the case where ContextKeyRoles is missing
func TestAdminOnly_NoRoles(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService_NoContext) // Use the no-context mock
	mockAuthz := new(AllowAllAuthzService)

	handler, _ := NewHandler(mockService, mockAuth, mockAuthz)

	// Wrap a dummy handler
	target := handler.adminOnly(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/admin/users", nil)
	rr := httptest.NewRecorder()

	target(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "Access denied")
}

func TestClaimsToMap_NilDates(t *testing.T) {
	claims := &identity.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "sub",
		},
		// Dates are nil
	}
	m := claimsToMap(claims)
	assert.Nil(t, m["nbf"])
	assert.Nil(t, m["exp"])
	assert.Nil(t, m["iat"])
	assert.Equal(t, "sub", m["sub"])
}

func TestAuthorized_InvalidJSONBody(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzService)

	handler, _ := NewHandler(mockService, mockAuth, mockAuthz)

	// We need to verify that Evaluate is called with nil Resource (or Resource with nil Data)
	// when body is invalid JSON.

	mockAuthz.On("Evaluate", mock.Anything, "default", "col/doc", "create", mock.MatchedBy(func(req identity.AuthzRequest) bool {
		// Resource should be nil because Unmarshal failed
		return req.Resource == nil
	}), mock.Anything).Return(true, nil)

	// Wrap a dummy handler
	target := handler.authorized(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, "create")

	req, _ := http.NewRequest("POST", "/api/v1/databases/default/documents/col/doc", bytes.NewBufferString("{invalid-json"))
	req.SetPathValue("database", "default")
	req.SetPathValue("path", "col/doc")

	rr := httptest.NewRecorder()
	target(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mockAuthz.AssertExpectations(t)
}

func TestClaimsToMap_NilClaims(t *testing.T) {
	var claims *identity.Claims
	m := claimsToMap(claims)
	assert.Nil(t, m)
}

// TestAuthorized_OwnerImplicitDBAdmin_SlugMatch tests the case where the owner's DBAdmin list
// already contains the database slug (not ID), and we verify that no duplicate is added.
func TestAuthorized_OwnerImplicitDBAdmin_SlugMatch(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzService)

	handler, _ := NewHandler(mockService, mockAuth, mockAuthz)

	// Set up authz to allow the request
	mockAuthz.On("Evaluate", mock.Anything, "db-123", "col/doc", "create", mock.MatchedBy(func(req identity.AuthzRequest) bool {
		// Verify that my-slug is in DBAdmin list (not duplicated)
		count := 0
		for _, admin := range req.Auth.DBAdmin {
			if admin == "my-slug" || admin == "db-123" {
				count++
			}
		}
		// Should have exactly one entry (my-slug - the original one)
		// The slug match should prevent adding db-123
		return count == 1
	}), mock.Anything).Return(true, nil)

	target := handler.authorized(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, "create")

	req, _ := http.NewRequest("POST", "/api/v1/databases/db-123/documents/col/doc", bytes.NewBufferString(`{"name":"test"}`))
	req.SetPathValue("database", "db-123")
	req.SetPathValue("path", "col/doc")

	// Create a database with slug
	slug := "my-slug"
	db := &database.Database{
		ID:      "db-123",
		Slug:    &slug,
		OwnerID: "user-123",
	}

	// Add database to context
	ctx := database.WithDatabase(req.Context(), db)

	// Add user ID (matches owner)
	ctx = context.WithValue(ctx, identity.ContextKeyUserID, "user-123")

	// Add DBAdmin list that already contains the slug
	ctx = context.WithValue(ctx, identity.ContextKeyDBAdmin, []string{"my-slug"})

	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	target(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mockAuthz.AssertExpectations(t)
}

// TestAuthorized_OwnerImplicitDBAdmin_IDMatch tests the case where the owner's DBAdmin list
// already contains the database ID, and we verify that no duplicate is added.
func TestAuthorized_OwnerImplicitDBAdmin_IDMatch(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzService)

	handler, _ := NewHandler(mockService, mockAuth, mockAuthz)

	// Set up authz to allow the request
	mockAuthz.On("Evaluate", mock.Anything, "db-123", "col/doc", "create", mock.MatchedBy(func(req identity.AuthzRequest) bool {
		// Verify that db-123 is in DBAdmin list exactly once
		count := 0
		for _, admin := range req.Auth.DBAdmin {
			if admin == "db-123" {
				count++
			}
		}
		return count == 1
	}), mock.Anything).Return(true, nil)

	target := handler.authorized(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, "create")

	req, _ := http.NewRequest("POST", "/api/v1/databases/db-123/documents/col/doc", bytes.NewBufferString(`{"name":"test"}`))
	req.SetPathValue("database", "db-123")
	req.SetPathValue("path", "col/doc")

	// Create a database with slug
	slug := "my-slug"
	db := &database.Database{
		ID:      "db-123",
		Slug:    &slug,
		OwnerID: "user-123",
	}

	// Add database to context
	ctx := database.WithDatabase(req.Context(), db)

	// Add user ID (matches owner)
	ctx = context.WithValue(ctx, identity.ContextKeyUserID, "user-123")

	// Add DBAdmin list that already contains the ID
	ctx = context.WithValue(ctx, identity.ContextKeyDBAdmin, []string{"db-123"})

	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	target(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mockAuthz.AssertExpectations(t)
}

// TestAuthorized_OwnerImplicitDBAdmin_NotInList tests the case where the owner's DBAdmin list
// does not contain the database, and we verify that the database ID is added.
func TestAuthorized_OwnerImplicitDBAdmin_NotInList(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzService)

	handler, _ := NewHandler(mockService, mockAuth, mockAuthz)

	// Set up authz to allow the request
	mockAuthz.On("Evaluate", mock.Anything, "db-123", "col/doc", "create", mock.MatchedBy(func(req identity.AuthzRequest) bool {
		// Verify that db-123 is in DBAdmin list (should have been added)
		for _, admin := range req.Auth.DBAdmin {
			if admin == "db-123" {
				return true
			}
		}
		return false
	}), mock.Anything).Return(true, nil)

	target := handler.authorized(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, "create")

	req, _ := http.NewRequest("POST", "/api/v1/databases/db-123/documents/col/doc", bytes.NewBufferString(`{"name":"test"}`))
	req.SetPathValue("database", "db-123")
	req.SetPathValue("path", "col/doc")

	// Create a database with slug
	slug := "my-slug"
	db := &database.Database{
		ID:      "db-123",
		Slug:    &slug,
		OwnerID: "user-123",
	}

	// Add database to context
	ctx := database.WithDatabase(req.Context(), db)

	// Add user ID (matches owner)
	ctx = context.WithValue(ctx, identity.ContextKeyUserID, "user-123")

	// Add DBAdmin list that does NOT contain the database
	ctx = context.WithValue(ctx, identity.ContextKeyDBAdmin, []string{"other-db"})

	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	target(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	mockAuthz.AssertExpectations(t)
}

// TestWithAuthRateLimit tests the auth rate limiting functionality
func TestWithAuthRateLimit_NoLimiter(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzService)

	handler, _ := NewHandler(mockService, mockAuth, mockAuthz)
	// No auth rate limiter set - should pass through

	called := false
	wrapped := handler.withAuthRateLimit(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("POST", "/auth/v1/login", nil)
	rr := httptest.NewRecorder()
	wrapped(rr, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// mockRateLimiter for testing
type mockRateLimiter struct {
	allowResult bool
}

func (m *mockRateLimiter) Allow(key string) bool {
	return m.allowResult
}

func (m *mockRateLimiter) Reset(key string) {
	// no-op for testing
}

func TestWithAuthRateLimit_Allowed(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzService)

	handler, _ := NewHandler(mockService, mockAuth, mockAuthz)
	handler.SetAuthRateLimiter(&mockRateLimiter{allowResult: true}, 60000000000) // 1 minute

	called := false
	wrapped := handler.withAuthRateLimit(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("POST", "/auth/v1/login", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rr := httptest.NewRecorder()
	wrapped(rr, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestWithAuthRateLimit_Denied(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzService)

	handler, _ := NewHandler(mockService, mockAuth, mockAuthz)
	handler.SetAuthRateLimiter(&mockRateLimiter{allowResult: false}, 60000000000) // 1 minute

	called := false
	wrapped := handler.withAuthRateLimit(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("POST", "/auth/v1/login", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rr := httptest.NewRecorder()
	wrapped(rr, req)

	assert.False(t, called)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Contains(t, rr.Body.String(), "RATE_LIMITED")
	assert.NotEmpty(t, rr.Header().Get("Retry-After"))
}

// TestNewHandler_WithOptions tests the option pattern for NewHandler
func TestNewHandler_WithOptions(t *testing.T) {
	mockService := new(MockQueryService)
	mockAuth := new(MockAuthService)
	mockAuthz := new(MockAuthzService)

	t.Run("WithDatabaseService option", func(t *testing.T) {
		// Using a stub that satisfies database.Service interface
		mockDB := &stubDatabaseService{}
		handler, err := NewHandler(mockService, mockAuth, mockAuthz,
			WithDatabaseService(mockDB))

		assert.NoError(t, err)
		assert.NotNil(t, handler)
		assert.NotNil(t, handler.database)
		assert.NotNil(t, handler.dbValidator)
	})

	t.Run("WithAuthRateLimiter option", func(t *testing.T) {
		limiter := &mockRateLimiter{allowResult: true}
		handler, err := NewHandler(mockService, mockAuth, mockAuthz,
			WithAuthRateLimiter(limiter, 60000000000))

		assert.NoError(t, err)
		assert.NotNil(t, handler)
		assert.NotNil(t, handler.authRateLimiter)
	})

	t.Run("Multiple options", func(t *testing.T) {
		mockDB := &stubDatabaseService{}
		limiter := &mockRateLimiter{allowResult: true}
		handler, err := NewHandler(mockService, mockAuth, mockAuthz,
			WithDatabaseService(mockDB),
			WithAuthRateLimiter(limiter, 60000000000))

		assert.NoError(t, err)
		assert.NotNil(t, handler)
		assert.NotNil(t, handler.database)
		assert.NotNil(t, handler.authRateLimiter)
	})

	t.Run("Nil database service does not create validator", func(t *testing.T) {
		handler, err := NewHandler(mockService, mockAuth, mockAuthz,
			WithDatabaseService(nil))

		assert.NoError(t, err)
		assert.NotNil(t, handler)
		assert.Nil(t, handler.database)
		assert.Nil(t, handler.dbValidator)
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
func (s *stubDatabaseService) ResolveDatabaseAuthoritative(ctx context.Context, identifier string) (*database.Database, error) {
	return s.ResolveDatabase(ctx, identifier)
}

func (s *stubDatabaseService) ValidateDatabase(ctx context.Context, identifier string) error {
	return nil
}
