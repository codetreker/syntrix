package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codetreker/syntrix/internal/core/identity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// AdminTestAuthService wraps MockAuthService to provide custom middleware for admin tests
type AdminTestAuthService struct {
	*MockAuthService
}

func (m *AdminTestAuthService) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate auth middleware by checking a header
		role := r.Header.Get("X-Role")
		if role == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), identity.ContextKeyRoles, []string{role})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *AdminTestAuthService) MiddlewareOptional(next http.Handler) http.Handler {
	return m.Middleware(next)
}

type errReadCloser struct{ err error }

func (e errReadCloser) Read(p []byte) (int, error) { return 0, e.err }
func (e errReadCloser) Close() error               { return nil }

func TestAdmin_ListUsers(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	// Mock ListUsers - no longer requires database parameter
	users := []*identity.User{
		{ID: "1", Username: "user1", Roles: []string{"user"}},
		{ID: "2", Username: "user2", Roles: []string{"admin"}},
	}
	mockAuth.On("ListUsers", mock.Anything, 50, 0).Return(users, nil)

	// Create Request - no database query param needed
	req := httptest.NewRequest("GET", "/admin/users", nil)
	req.Header.Set("X-Role", "admin") // Mock middleware check
	w := httptest.NewRecorder()

	// Serve
	server.ServeHTTP(w, req)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	var respUsers []*identity.User
	err := json.NewDecoder(w.Body).Decode(&respUsers)
	assert.NoError(t, err)
	assert.Len(t, respUsers, 2)
	assert.Equal(t, "user1", respUsers[0].Username)
}

func TestAdmin_ListUsers_WithPaging(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	users := []*identity.User{}
	mockAuth.On("ListUsers", mock.Anything, 10, 5).Return(users, nil)

	req := httptest.NewRequest("GET", "/admin/users?limit=10&offset=5", nil)
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	mockAuth.AssertExpectations(t)
}

func TestAdmin_UpdateUser(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	// Mock UpdateUser - no longer requires database parameter
	mockAuth.On("UpdateUser", mock.Anything, "123", []string{"admin"}, []string(nil), true).Return(nil)

	// Create Request - no database query param needed
	body := map[string]interface{}{
		"roles":    []string{"admin"},
		"disabled": true,
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/admin/users/123", bytes.NewReader(bodyBytes))
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	// Serve
	server.ServeHTTP(w, req)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	mockAuth.AssertExpectations(t)
}

func TestAdmin_UpdateUser_InvalidBody(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	req := httptest.NewRequest("PATCH", "/admin/users/123", bytes.NewBufferString("{invalid"))
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	mockAuth.AssertNotCalled(t, "UpdateUser", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestAdmin_UpdateUser_Error(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	body := map[string]interface{}{
		"roles":    []string{"user"},
		"disabled": false,
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("PATCH", "/admin/users/123", bytes.NewReader(bodyBytes))
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	mockAuth.On("UpdateUser", mock.Anything, "123", []string{"user"}, []string(nil), false).Return(errors.New("fail"))

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockAuth.AssertExpectations(t)
}

func TestAdmin_PushRules(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	// Mock UpdateRules
	rulesContent := []byte("rules_version: '1'")
	mockAuthz.On("UpdateRules", "default", rulesContent).Return(nil)

	// Create Request
	req := httptest.NewRequest("POST", "/admin/rules/push?database=default", bytes.NewReader(rulesContent))
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	// Serve
	server.ServeHTTP(w, req)

	// Assert
	assert.Equal(t, http.StatusOK, w.Code)
	mockAuthz.AssertExpectations(t)
}

func TestAdmin_PushRules_Invalid(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	rulesContent := []byte("bad")
	mockAuthz.On("UpdateRules", "default", rulesContent).Return(errors.New("bad rules"))

	req := httptest.NewRequest("POST", "/admin/rules/push?database=default", bytes.NewReader(rulesContent))
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	mockAuthz.AssertExpectations(t)
}

func TestAdmin_PushRules_ReadError(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	req := httptest.NewRequest("POST", "/admin/rules/push", nil)
	req.Body = errReadCloser{err: errors.New("read error")}
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	mockAuthz.AssertNotCalled(t, "UpdateRules", mock.Anything)
}

func TestAdmin_ListUsers_Error(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	mockAuth.On("ListUsers", mock.Anything, 50, 0).Return(nil, errors.New("boom"))

	req := httptest.NewRequest("GET", "/admin/users", nil)
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockAuth.AssertExpectations(t)
}

func TestAdmin_GetRules(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	ruleSet := &identity.RuleSet{Version: "1"}
	mockAuthz.On("GetRulesForDatabase", "default").Return(ruleSet)

	req := httptest.NewRequest("GET", "/admin/rules", nil)
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp identity.RuleSet
	assert.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "1", resp.Version)
	mockAuthz.AssertExpectations(t)
}

func TestAdmin_GetRules_NotFound(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	mockAuthz.On("GetRulesForDatabase", "nonexistent").Return(nil)

	req := httptest.NewRequest("GET", "/admin/rules?database=nonexistent", nil)
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "No rules found")
	mockAuthz.AssertExpectations(t)
}

func TestAdmin_Health(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	req := httptest.NewRequest("GET", "/admin/health", nil)
	req.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "OK", w.Body.String())
}

func TestAdmin_AccessDenied(t *testing.T) {
	mockAuth := &AdminTestAuthService{MockAuthService: new(MockAuthService)}
	mockAuthz := new(MockAuthzService)
	server := createTestServer(nil, mockAuth, mockAuthz)

	// Create Request with non-admin role
	req := httptest.NewRequest("GET", "/admin/users", nil)
	req.Header.Set("X-Role", "user")
	w := httptest.NewRecorder()

	// Serve
	server.ServeHTTP(w, req)

	// Assert
	assert.Equal(t, http.StatusForbidden, w.Code)
}
