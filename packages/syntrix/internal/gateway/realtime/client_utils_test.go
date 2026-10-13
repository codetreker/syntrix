package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codetreker/syntrix/internal/ctxkeys"
	api_config "github.com/codetreker/syntrix/internal/gateway/config"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/stretchr/testify/assert"
)

func TestDatabaseFromContextMust(t *testing.T) {
	// Case 1: Database present (using internal context key)
	ctx := context.WithValue(context.Background(), contextKeyDatabase, "t1")
	w := httptest.NewRecorder()
	database := databaseFromContextMust(ctx, w)
	assert.Equal(t, "t1", database)
	assert.Equal(t, 200, w.Code)

	// Case 2: Database missing
	ctx2 := context.Background()
	w2 := httptest.NewRecorder()
	database2 := databaseFromContextMust(ctx2, w2)
	assert.Equal(t, "", database2)
	assert.Equal(t, 401, w2.Code)
}

func TestHasSystemRoleFromClaims(t *testing.T) {
	// Case 1: Nil claims
	assert.False(t, hasSystemRoleFromClaims(nil))

	// Case 2: No system role
	claims1 := &identity.Claims{Roles: []string{"user", "admin"}}
	assert.False(t, hasSystemRoleFromClaims(claims1))

	// Case 3: System role present
	claims2 := &identity.Claims{Roles: []string{"user", "system"}}
	assert.True(t, hasSystemRoleFromClaims(claims2))

	// Case 4: System role case insensitive
	claims3 := &identity.Claims{Roles: []string{"SYSTEM"}}
	assert.True(t, hasSystemRoleFromClaims(claims3))
}

func TestTokenFromQuery(t *testing.T) {
	// Case 1: Nil request
	assert.Equal(t, "", tokenFromQuery(nil))

	// Case 2: access_token
	req1 := httptest.NewRequest("GET", "/?access_token=abc", nil)
	assert.Equal(t, "abc", tokenFromQuery(req1))

	// Case 3: token
	req2 := httptest.NewRequest("GET", "/?token=xyz", nil)
	assert.Equal(t, "xyz", tokenFromQuery(req2))

	// Case 4: Both (access_token takes precedence)
	req3 := httptest.NewRequest("GET", "/?access_token=abc&token=xyz", nil)
	assert.Equal(t, "abc", tokenFromQuery(req3))

	// Case 5: None
	req4 := httptest.NewRequest("GET", "/", nil)
	assert.Equal(t, "", tokenFromQuery(req4))
}

func TestHasCredentials(t *testing.T) {
	// Case 1: Nil request
	assert.False(t, hasCredentials(nil))

	// Case 2: No auth header
	req1 := httptest.NewRequest("GET", "/", nil)
	assert.False(t, hasCredentials(req1))

	// Case 3: Auth header present
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Authorization", "Bearer token")
	assert.True(t, hasCredentials(req2))
}

func originRequest(value, host string, credentialed bool) *http.Request {
	r := httptest.NewRequest("GET", "http://"+host+"/", nil)
	if value != "" {
		r.Header.Set("Origin", value)
	}
	if credentialed {
		r.Header.Set("Authorization", "Bearer test")
	}
	return r
}

func TestCheckRequestOrigin(t *testing.T) {
	cases := []struct {
		origin, host          string
		cfg                   api_config.RealtimeConfig
		credentialed, allowed bool
	}{
		{"", "host", api_config.RealtimeConfig{}, false, true},
		{"", "host", api_config.RealtimeConfig{AllowDevOrigin: true}, true, true},
		{"", "host", api_config.RealtimeConfig{}, true, false},
		{":", "host", api_config.RealtimeConfig{}, false, false},
		{"http://host:8080", "host:8080", api_config.RealtimeConfig{}, false, true},
		{"http://host:3000", "host:8080", api_config.RealtimeConfig{}, false, false},
		{"http://localhost:3000", "host", api_config.RealtimeConfig{AllowDevOrigin: true}, false, true},
		{"http://127.0.0.1:3000", "host", api_config.RealtimeConfig{AllowDevOrigin: true}, false, true},
		{"http://localhost:3000", "host", api_config.RealtimeConfig{}, false, false},
		{"http://example.com", "host", api_config.RealtimeConfig{AllowedOrigins: []string{"http://example.com"}}, false, true},
		{"http://other.com", "host", api_config.RealtimeConfig{AllowedOrigins: []string{"http://example.com"}}, false, false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.allowed, checkRequestOrigin(originRequest(tc.origin, tc.host, tc.credentialed), tc.cfg) == nil)
	}
}

func TestDatabaseFromContext(t *testing.T) {
	// Case 1: Nil context
	database, allowAll := databaseFromContext(nil)
	assert.Equal(t, "", database)
	assert.False(t, allowAll)

	// Case 2: Database in context (using internal context key)
	ctx1 := context.WithValue(context.Background(), contextKeyDatabase, "database1")
	database, allowAll = databaseFromContext(ctx1)
	assert.Equal(t, "database1", database)
	assert.False(t, allowAll)

	// Case 3: System role in claims makes allowAll true
	claimsSystem := &identity.Claims{Roles: []string{"system"}}
	ctx2 := context.WithValue(context.Background(), ctxkeys.KeyClaims, claimsSystem)
	ctx2 = context.WithValue(ctx2, contextKeyDatabase, "database2")
	database, allowAll = databaseFromContext(ctx2)
	assert.Equal(t, "database2", database)
	assert.True(t, allowAll)
}
