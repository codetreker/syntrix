package authn

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/identity/config"
	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestListUsers_Coverage(t *testing.T) {
	t.Parallel()
	mockStorage := new(MockStorage)
	cfg := config.AuthNConfig{
		PrivateKeyFile: getTestKeyPath(t),
		AccessTokenTTL: time.Hour,
	}
	svc, err := newTestAccountService(cfg, mockStorage, mockStorage)
	require.NoError(t, err)

	t.Run("Success", func(t *testing.T) {
		ctx := context.Background()
		expectedUsers := []*repository.UserRecord{{ID: "u1", Username: "user1"}}

		mockStorage.On("ListUsers", ctx, 10, 0).Return(expectedUsers, nil).Once()

		users, err := svc.ListUsers(ctx, systemActor(t, svc), 10, 0)
		assert.NoError(t, err)
		assert.Equal(t, []*identity.User{{ID: "u1", Username: "user1"}}, users)
		mockStorage.AssertExpectations(t)
	})

	t.Run("Storage Error", func(t *testing.T) {
		ctx := context.Background()

		mockStorage.On("ListUsers", ctx, 10, 0).Return(nil, errors.New("db error")).Once()

		users, err := svc.ListUsers(ctx, systemActor(t, svc), 10, 0)
		assert.Error(t, err)
		assert.Nil(t, users)
		mockStorage.AssertExpectations(t)
	})
}

func TestUpdateUser_Coverage(t *testing.T) {
	t.Parallel()
	mockStorage := new(MockStorage)
	cfg := config.AuthNConfig{
		PrivateKeyFile: getTestKeyPath(t),
		AccessTokenTTL: time.Hour,
	}
	svc, err := newTestAccountService(cfg, mockStorage, mockStorage)
	require.NoError(t, err)

	t.Run("repository.UserRecord Not Found", func(t *testing.T) {
		ctx := context.Background()

		mockStorage.On("GetUserByID", ctx, "u1").Return(nil, errors.New("not found")).Once()

		err := svc.UpdateUser(ctx, systemActor(t, svc), "u1", []string{"admin"}, nil, false)
		assert.Error(t, err)
		mockStorage.AssertExpectations(t)
	})

	t.Run("Success", func(t *testing.T) {
		ctx := context.Background()
		user := &repository.UserRecord{ID: "u1", Roles: []string{"user"}}

		mockStorage.On("GetUserByID", ctx, "u1").Return(user, nil).Once()
		mockStorage.On("UpdateUser", ctx, mock.MatchedBy(func(u *repository.UserRecord) bool {
			return u.ID == "u1" && u.Disabled == true && len(u.Roles) == 1 && u.Roles[0] == "admin"
		})).Return(nil).Once()

		err := svc.UpdateUser(ctx, systemActor(t, svc), "u1", []string{"admin"}, nil, true)
		assert.NoError(t, err)
		mockStorage.AssertExpectations(t)
	})

	t.Run("Success with DBAdmin", func(t *testing.T) {
		ctx := context.Background()
		user := &repository.UserRecord{ID: "u2", Roles: []string{"user"}}

		mockStorage.On("GetUserByID", ctx, "u2").Return(user, nil).Once()
		mockStorage.On("UpdateUser", ctx, mock.MatchedBy(func(u *repository.UserRecord) bool {
			return u.ID == "u2" && len(u.DBAdmin) == 2 && u.DBAdmin[0] == "db1" && u.DBAdmin[1] == "db2"
		})).Return(nil).Once()

		err := svc.UpdateUser(ctx, systemActor(t, svc), "u2", []string{"user"}, []string{"db1", "db2"}, false)
		assert.NoError(t, err)
		mockStorage.AssertExpectations(t)
	})
}

func systemActor(t *testing.T, svc *accountService) *identity.VerifiedIdentity {
	t.Helper()
	token, err := (&systemTokenIssuer{privateKey: svc.signer.privateKey, accessTTL: svc.signer.accessTTL}).GenerateSystemToken("test-admin")
	require.NoError(t, err)
	actor, err := svc.verifier.VerifyToken(token)
	require.NoError(t, err)
	return actor
}
