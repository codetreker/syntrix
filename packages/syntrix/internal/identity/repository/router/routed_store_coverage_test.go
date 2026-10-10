package router

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/stretchr/testify/assert"
)

func TestRoutedUserStore_Coverage(t *testing.T) {
	ctx := context.Background()
	database := "default"
	errSelect := errors.New("select error")

	t.Run("CreateUser Select Error", func(t *testing.T) {
		router := new(mockUserRouter)
		router.On("Select", database, repository.OpWrite).Return(nil, errSelect)

		rs := NewRoutedUserStore(router)
		err := rs.CreateUser(ctx, &repository.UserRecord{})

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("GetUserByUsername Select Error", func(t *testing.T) {
		router := new(mockUserRouter)
		router.On("Select", database, repository.OpRead).Return(nil, errSelect)

		rs := NewRoutedUserStore(router)
		_, err := rs.GetUserByUsername(ctx, "user")

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("GetUserByID Select Error", func(t *testing.T) {
		router := new(mockUserRouter)
		router.On("Select", "default", repository.OpRead).Return(nil, errSelect)

		rs := NewRoutedUserStore(router)
		_, err := rs.GetUserByID(ctx, "id")

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("ListUsers Select Error", func(t *testing.T) {
		router := new(mockUserRouter)
		router.On("Select", "default", repository.OpRead).Return(nil, errSelect)

		rs := NewRoutedUserStore(router)
		_, err := rs.ListUsers(ctx, 10, 0)

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("UpdateUser Select Error", func(t *testing.T) {
		router := new(mockUserRouter)
		router.On("Select", "default", repository.OpWrite).Return(nil, errSelect)

		rs := NewRoutedUserStore(router)
		err := rs.UpdateUser(ctx, &repository.UserRecord{})

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("UpdateUserLoginStats Select Error", func(t *testing.T) {
		router := new(mockUserRouter)
		router.On("Select", database, repository.OpWrite).Return(nil, errSelect)

		rs := NewRoutedUserStore(router)
		err := rs.UpdateUserLoginStats(ctx, "id", time.Now(), 0, time.Time{})

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("EnsureIndexes Select Error", func(t *testing.T) {
		router := new(mockUserRouter)
		router.On("Select", "default", repository.OpWrite).Return(nil, errSelect)

		rs := NewRoutedUserStore(router)
		err := rs.EnsureIndexes(ctx)

		assert.ErrorIs(t, err, errSelect)
	})
}

func TestRoutedRevocationStore_Coverage(t *testing.T) {
	ctx := context.Background()
	database := "default"
	errSelect := errors.New("select error")

	t.Run("RevokeToken Select Error", func(t *testing.T) {
		router := new(mockRevRouter)
		router.On("Select", database, repository.OpWrite).Return(nil, errSelect)

		rs := NewRoutedRevocationStore(router)
		err := rs.RevokeToken(ctx, "jti", time.Now())

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("RevokeTokenImmediate Select Error", func(t *testing.T) {
		router := new(mockRevRouter)
		router.On("Select", database, repository.OpWrite).Return(nil, errSelect)

		rs := NewRoutedRevocationStore(router)
		err := rs.RevokeTokenImmediate(ctx, "jti", time.Now())

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("IsRevoked Select Error", func(t *testing.T) {
		router := new(mockRevRouter)
		router.On("Select", database, repository.OpRead).Return(nil, errSelect)

		rs := NewRoutedRevocationStore(router)
		_, err := rs.IsRevoked(ctx, "jti", time.Minute)

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("EnsureIndexes Select Error", func(t *testing.T) {
		router := new(mockRevRouter)
		router.On("Select", "default", repository.OpWrite).Return(nil, errSelect)

		rs := NewRoutedRevocationStore(router)
		err := rs.EnsureIndexes(ctx)

		assert.ErrorIs(t, err, errSelect)
	})

	t.Run("RevokeTokenIfNotRevoked Select Error", func(t *testing.T) {
		router := new(mockRevRouter)
		router.On("Select", database, repository.OpWrite).Return(nil, errSelect)

		rs := NewRoutedRevocationStore(router)
		err := rs.RevokeTokenIfNotRevoked(ctx, "jti", time.Now(), time.Minute)

		assert.ErrorIs(t, err, errSelect)
	})
}
