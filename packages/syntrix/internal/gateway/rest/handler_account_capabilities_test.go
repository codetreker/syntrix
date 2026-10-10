package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/authn"
	identityconfig "github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestGatewayAdminUsesAccountVerifierAuthority(t *testing.T) {
	store := new(MockAuthStorage)
	accounts, verifier, issuer, err := authn.NewServices(identityconfig.AuthNConfig{PrivateKeyFile: filepath.Join(t.TempDir(), "key.pem"), AccessTokenTTL: time.Hour}, store, store)
	require.NoError(t, err)
	handler, err := NewHandler(nil, accounts, verifier, new(AllowAllAuthzService))
	require.NoError(t, err)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	token, err := issuer.GenerateSystemToken("gateway-admin")
	require.NoError(t, err)
	store.On("ListUsers", mock.Anything, 50, 0).Return([]*storage.User{{ID: "user", PasswordHash: "secret", PasswordAlgo: "argon2id"}}, nil).Once()
	request := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), `"password_hash":""`)
	assert.Contains(t, response.Body.String(), `"password_algo":""`)
	assert.NotContains(t, response.Body.String(), "secret")
	foreignVerifier, err := identity.NewVerifier(func(string) (*identity.Claims, error) { return &identity.Claims{Roles: []string{"admin"}}, nil })
	require.NoError(t, err)
	foreignActor, err := foreignVerifier.VerifyToken("token")
	require.NoError(t, err)
	_, err = accounts.ListUsers(context.Background(), foreignActor, 50, 0)
	assert.ErrorIs(t, err, identity.ErrAdminRequired)
	store.AssertExpectations(t)
}
