package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codetreker/syntrix/internal/identity"
	"github.com/stretchr/testify/assert"
)

func TestNewHandler_NilArgs(t *testing.T) {
	mockAuthz := new(AllowAllAuthzService)

	// Test error when auth is nil
	_, err := NewHandler(&MockQueryService{}, nil, nil, mockAuthz)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "authn service cannot be nil")

	_, err = NewHandler(&MockQueryService{}, new(MockAuthService), nil, mockAuthz)
	assert.EqualError(t, err, "token verifier cannot be nil")

	// Test error when authz is nil
	_, err = NewHandler(&MockQueryService{}, new(MockAuthService), new(MockAuthService), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "authz service cannot be nil")
}

func TestGetDatabase(t *testing.T) {
	h := &Handler{}

	// Case 1: Database in URL path
	req := httptest.NewRequest("GET", "/api/v1/databases/mydb/documents/col/doc", nil)
	req.SetPathValue("database", "mydb")
	database, err := h.getDatabase(req)
	assert.NoError(t, err)
	assert.Equal(t, "mydb", database)

	// Case 2: Database missing
	req2 := httptest.NewRequest("GET", "/", nil)
	database2, err2 := h.getDatabase(req2)
	assert.Error(t, err2)
	assert.Equal(t, "", database2)
}

func TestDatabaseOrError(t *testing.T) {
	h := &Handler{}

	// Case 1: Database in URL path
	req := httptest.NewRequest("GET", "/api/v1/databases/t1/documents/col/doc", nil)
	req.SetPathValue("database", "t1")
	w := httptest.NewRecorder()
	database, ok := h.databaseOrError(w, req)
	assert.True(t, ok)
	assert.Equal(t, "t1", database)
	assert.Equal(t, http.StatusOK, w.Code)

	// Case 2: Database missing
	req2 := httptest.NewRequest("GET", "/", nil)
	w2 := httptest.NewRecorder()
	database2, ok2 := h.databaseOrError(w2, req2)
	assert.False(t, ok2)
	assert.Equal(t, "", database2)
	assert.Equal(t, http.StatusUnauthorized, w2.Code)
}

func TestClaimsToMap(t *testing.T) {
	// Case 1: Nil claims
	assert.Nil(t, claimsToMap(nil))

	// Case 2: Valid claims (without Database and TenantID which are removed)
	claims := &identity.Claims{
		UserID:   "u1",
		Username: "user1",
		Roles:    []string{"admin"},
	}
	m := claimsToMap(claims)
	assert.Equal(t, "u1", m["oid"])
	assert.Equal(t, "user1", m["username"])
	assert.Equal(t, []string{"admin"}, m["roles"])
}
