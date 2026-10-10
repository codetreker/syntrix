package authn

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestAccountCommandsRequireOwnAdminActor(t *testing.T) {
	store := new(MockStorage)
	svc, err := newTestAccountService(config.AuthNConfig{PrivateKeyFile: getTestKeyPath(t), AccessTokenTTL: time.Hour}, store, store)
	require.NoError(t, err)
	foreignVerifier, err := identity.NewVerifier(func(string) (*identity.Claims, error) { return &identity.Claims{Roles: []string{"admin"}}, nil })
	require.NoError(t, err)
	foreign, err := foreignVerifier.VerifyToken("token")
	require.NoError(t, err)
	for _, actor := range []*identity.VerifiedIdentity{nil, {}, foreign} {
		_, err := svc.ListUsers(context.Background(), actor, 10, 0)
		assert.ErrorIs(t, err, identity.ErrAdminRequired)
		assert.ErrorIs(t, svc.UpdateUser(context.Background(), actor, "target", []string{"admin"}, nil, true), identity.ErrAdminRequired)
	}
	for _, roles := range [][]string{{"user"}, {"Admin"}, {"SYSTEM"}, {"admin"}, {"system"}} {
		tokenPair, err := svc.signer.generateTokenPair(&User{ID: "actor", Roles: roles})
		require.NoError(t, err)
		actor, err := svc.verifier.VerifyToken(tokenPair.AccessToken)
		require.NoError(t, err)
		projection := actor.Claims()
		projection.Roles[0] = "admin"
		if roles[0] == "admin" || roles[0] == "system" {
			store.On("ListUsers", mock.Anything, 10, 0).Return([]*User{}, nil).Once()
			_, err = svc.ListUsers(context.Background(), actor, 10, 0)
			require.NoError(t, err)
		} else {
			_, err = svc.ListUsers(context.Background(), actor, 10, 0)
			assert.ErrorIs(t, err, identity.ErrAdminRequired)
			assert.ErrorIs(t, svc.UpdateUser(context.Background(), actor, "target", nil, nil, false), identity.ErrAdminRequired)
		}
	}
	store.AssertNotCalled(t, "GetUserByID", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "UpdateUser", mock.Anything, mock.Anything)
	store.AssertExpectations(t)
}

func TestAccountUserViewIsDetached(t *testing.T) {
	store := new(MockStorage)
	svc, err := newTestAccountService(config.AuthNConfig{PrivateKeyFile: getTestKeyPath(t), AccessTokenTTL: time.Hour}, store, store)
	require.NoError(t, err)
	actor := systemActor(t, svc)
	now := time.Now()
	persisted := &User{ID: "id", Username: "user", PasswordHash: "secret", PasswordAlgo: "argon2id", Roles: []string{"user"}, DBAdmin: []string{"db"},
		CreatedAt: now, UpdatedAt: now, LastLoginAt: now, LoginAttempts: 3, LockoutUntil: now, Disabled: true,
		Profile: map[string]interface{}{"nested": map[string]interface{}{"rows": []interface{}{map[string]interface{}{"name": "original"}}},
			"typed": []map[string]string{{"name": "original"}}, "array": [1][]string{{"original"}}, "nil": nil, "nilmap": map[string]string(nil), "nilslice": []string(nil)},
	}
	store.On("ListUsers", mock.Anything, 10, 0).Return([]*User{persisted}, nil).Once()
	views, err := svc.ListUsers(context.Background(), actor, 10, 0)
	require.NoError(t, err)
	view := views[0]
	assert.Equal(t, persisted.ID, view.ID)
	assert.Equal(t, persisted.Username, view.Username)
	assert.Equal(t, persisted.CreatedAt, view.CreatedAt)
	assert.Equal(t, persisted.UpdatedAt, view.UpdatedAt)
	assert.Equal(t, persisted.LastLoginAt, view.LastLoginAt)
	assert.Equal(t, persisted.LockoutUntil, view.LockoutUntil)
	assert.Equal(t, persisted.LoginAttempts, view.LoginAttempts)
	assert.Equal(t, persisted.Disabled, view.Disabled)
	assert.Equal(t, persisted.Profile, view.Profile)
	encoded, err := json.Marshal(view)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "password")
	view.Roles[0], view.DBAdmin[0] = "admin", "changed"
	view.Profile["nested"].(map[string]interface{})["rows"].([]interface{})[0].(map[string]interface{})["name"] = "changed"
	view.Profile["typed"].([]map[string]string)[0]["name"] = "changed"
	view.Profile["array"].([1][]string)[0][0] = "changed"
	assert.Equal(t, "user", persisted.Roles[0])
	assert.Equal(t, "db", persisted.DBAdmin[0])
	assert.Equal(t, "original", persisted.Profile["nested"].(map[string]interface{})["rows"].([]interface{})[0].(map[string]interface{})["name"])
	assert.Equal(t, "original", persisted.Profile["typed"].([]map[string]string)[0]["name"])
	assert.Equal(t, "original", persisted.Profile["array"].([1][]string)[0][0])
	assert.Equal(t, "secret", persisted.PasswordHash)
	assert.Equal(t, "argon2id", persisted.PasswordAlgo)
	for _, source := range [][]*User{nil, {}, {{ID: "nil"}}, {{ID: "empty", Roles: []string{}, DBAdmin: []string{}, Profile: map[string]interface{}{}}}} {
		store.On("ListUsers", mock.Anything, 10, 0).Return(source, nil).Once()
		views, err := svc.ListUsers(context.Background(), actor, 10, 0)
		require.NoError(t, err)
		assert.Equal(t, source == nil, views == nil)
		if len(source) != 0 {
			assert.Equal(t, source[0].Roles == nil, views[0].Roles == nil)
			assert.Equal(t, source[0].DBAdmin == nil, views[0].DBAdmin == nil)
			assert.Equal(t, source[0].Profile == nil, views[0].Profile == nil)
		}
	}
	store.AssertExpectations(t)
}

func TestCanonicalUserErrors(t *testing.T) {
	assert.Same(t, identity.ErrUserNotFound, ErrUserNotFound)
	assert.Same(t, identity.ErrUserExists, ErrUserExists)
	assert.Same(t, identity.ErrInvalidToken, ErrInvalidToken)
}
