package authn

import (
	"context"
	"errors"
	"testing"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestListUsers_Coverage(t *testing.T) {
	t.Parallel()
	mockStorage := new(MockStorage)
	cfg := config.AuthNConfig{
		PrivateKeyFile: getTestKeyPath(t),
	}
	svc, err := NewAuthService(cfg, mockStorage, mockStorage)
	require.NoError(t, err)

	t.Run("Success", func(t *testing.T) {
		ctx := context.Background()
		expectedUsers := []*User{{ID: "u1", Username: "user1"}}

		mockStorage.On("ListUsers", ctx, 10, 0).Return(expectedUsers, nil).Once()

		users, err := svc.ListUsers(ctx, 10, 0)
		assert.NoError(t, err)
		assert.Equal(t, expectedUsers, users)
		mockStorage.AssertExpectations(t)
	})

	t.Run("Storage Error", func(t *testing.T) {
		ctx := context.Background()

		mockStorage.On("ListUsers", ctx, 10, 0).Return(nil, errors.New("db error")).Once()

		users, err := svc.ListUsers(ctx, 10, 0)
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
	}
	svc, err := NewAuthService(cfg, mockStorage, mockStorage)
	require.NoError(t, err)

	t.Run("User Not Found", func(t *testing.T) {
		ctx := context.Background()

		mockStorage.On("GetUserByID", ctx, "u1").Return(nil, errors.New("not found")).Once()

		err := svc.UpdateUser(ctx, "u1", []string{"admin"}, nil, false)
		assert.Error(t, err)
		mockStorage.AssertExpectations(t)
	})

	t.Run("Success", func(t *testing.T) {
		ctx := context.Background()
		user := &User{ID: "u1", Roles: []string{"user"}}

		mockStorage.On("GetUserByID", ctx, "u1").Return(user, nil).Once()
		mockStorage.On("UpdateUser", ctx, mock.MatchedBy(func(u *User) bool {
			return u.ID == "u1" && u.Disabled == true && len(u.Roles) == 1 && u.Roles[0] == "admin"
		})).Return(nil).Once()

		err := svc.UpdateUser(ctx, "u1", []string{"admin"}, nil, true)
		assert.NoError(t, err)
		mockStorage.AssertExpectations(t)
	})

	t.Run("Success with DBAdmin", func(t *testing.T) {
		ctx := context.Background()
		user := &User{ID: "u2", Roles: []string{"user"}}

		mockStorage.On("GetUserByID", ctx, "u2").Return(user, nil).Once()
		mockStorage.On("UpdateUser", ctx, mock.MatchedBy(func(u *User) bool {
			return u.ID == "u2" && len(u.DBAdmin) == 2 && u.DBAdmin[0] == "db1" && u.DBAdmin[1] == "db2"
		})).Return(nil).Once()

		err := svc.UpdateUser(ctx, "u2", []string{"user"}, []string{"db1", "db2"}, false)
		assert.NoError(t, err)
		mockStorage.AssertExpectations(t)
	})
}
