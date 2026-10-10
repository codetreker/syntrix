package authn

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestServicesSeparateTokenCapabilities(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "private.pem")
	store := new(MockStorage)
	accounts, verifier, issuer, err := NewServices(config.AuthNConfig{PrivateKeyFile: keyFile, AccessTokenTTL: time.Hour, RefreshTokenTTL: 24 * time.Hour, AuthCodeTTL: 2 * time.Minute}, store, store)
	require.NoError(t, err)
	require.Same(t, verifier, accounts.(*accountService).verifier)
	assert.Implements(t, (*identity.AccountService)(nil), accounts)
	assert.NotImplements(t, (*identity.TokenVerifier)(nil), accounts)
	assert.NotImplements(t, (*identity.SystemTokenIssuer)(nil), accounts)
	assert.Implements(t, (*identity.SystemTokenIssuer)(nil), issuer)
	assert.NotImplements(t, (*identity.TokenVerifier)(nil), issuer)
	assert.NotImplements(t, (*identity.AccountService)(nil), issuer)
	issuerType := reflect.TypeOf(issuer)
	require.Equal(t, 1, issuerType.NumMethod())
	assert.Equal(t, "GenerateSystemToken", issuerType.Method(0).Name)
	assert.Zero(t, reflect.TypeOf(accounts.(*accountService).signer).NumMethod())
	token, err := issuer.GenerateSystemToken("worker")
	require.NoError(t, err)
	require.NoError(t, os.Remove(keyFile))
	actor, err := verifier.VerifyToken(token)
	require.NoError(t, err)
	assert.Equal(t, "system:worker", actor.Claims().Subject)
	assert.Empty(t, store.Calls, "public validation does not access user or revocation storage")
	store.On("ListUsers", mock.Anything, 10, 0).Return([]*User{}, nil).Once()
	_, err = accounts.ListUsers(context.Background(), actor, 10, 0)
	require.NoError(t, err)
	foreign, err := identity.NewPublicTokenVerifier(&accounts.(*accountService).signer.privateKey.PublicKey)
	require.NoError(t, err)
	foreignActor, err := foreign.VerifyToken(token)
	require.NoError(t, err)
	_, err = accounts.ListUsers(context.Background(), foreignActor, 10, 0)
	assert.ErrorIs(t, err, identity.ErrAdminRequired)
	store.AssertExpectations(t)
}

func TestRefreshAndLogoutPreserveOverlapAndOrder(t *testing.T) {
	store := new(MockStorage)
	accounts, verifier, _, err := NewServices(config.AuthNConfig{PrivateKeyFile: getTestKeyPath(t), AccessTokenTTL: time.Hour, RefreshTokenTTL: 24 * time.Hour, AuthCodeTTL: 5 * time.Minute}, store, store)
	require.NoError(t, err)
	user := &User{ID: "user", Username: "user", Roles: []string{"user"}, DBAdmin: []string{"db"}}
	pair, err := accounts.(*accountService).signer.generateTokenPair(user)
	require.NoError(t, err)
	actor, err := verifier.VerifyToken(pair.RefreshToken)
	require.NoError(t, err)
	claims := actor.Claims()
	order := []string{}
	store.On("RevokeTokenIfNotRevoked", mock.Anything, claims.ID, claims.ExpiresAt.Time, 5*time.Minute).
		Run(func(mock.Arguments) { order = append(order, "revoke") }).Return(nil).Once()
	store.On("GetUserByID", mock.Anything, user.ID).Run(func(mock.Arguments) { order = append(order, "user") }).Return(user, nil).Once()
	refreshed, err := accounts.Refresh(context.Background(), RefreshRequest{RefreshToken: pair.RefreshToken})
	require.NoError(t, err)
	assert.Equal(t, []string{"revoke", "user"}, order)
	refreshedActor, err := verifier.VerifyToken(refreshed.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, user.ID, refreshedActor.Claims().Subject)
	store.On("RevokeTokenImmediate", mock.Anything, claims.ID, claims.ExpiresAt.Time).Return(nil).Once()
	require.NoError(t, accounts.Logout(context.Background(), pair.RefreshToken))
	store.AssertExpectations(t)
}
