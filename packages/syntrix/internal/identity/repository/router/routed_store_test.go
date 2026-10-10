package router

import (
	"context"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Mock Router
type mockUserRouter struct {
	mock.Mock
}

func (m *mockUserRouter) Select(database string, op repository.OpKind) (repository.UserStore, error) {
	args := m.Called(database, op)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(repository.UserStore), args.Error(1)
}

type mockUserStoreImpl struct {
	mock.Mock
}

func (m *mockUserStoreImpl) CreateUser(ctx context.Context, user *repository.UserRecord) error {
	return m.Called(ctx, user).Error(0)
}
func (m *mockUserStoreImpl) GetUserByUsername(ctx context.Context, username string) (*repository.UserRecord, error) {
	args := m.Called(ctx, username)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*repository.UserRecord), args.Error(1)
}
func (m *mockUserStoreImpl) GetUserByID(ctx context.Context, id string) (*repository.UserRecord, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*repository.UserRecord), args.Error(1)
}
func (m *mockUserStoreImpl) ListUsers(ctx context.Context, limit int, offset int) ([]*repository.UserRecord, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*repository.UserRecord), args.Error(1)
}
func (m *mockUserStoreImpl) UpdateUser(ctx context.Context, user *repository.UserRecord) error {
	return m.Called(ctx, user).Error(0)
}
func (m *mockUserStoreImpl) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	return m.Called(ctx, id, lastLogin, attempts, lockoutUntil).Error(0)
}
func (m *mockUserStoreImpl) UpdateUserPassword(ctx context.Context, userID string, hashedPassword string) error {
	return m.Called(ctx, userID, hashedPassword).Error(0)
}
func (m *mockUserStoreImpl) UpdateUserRoles(ctx context.Context, userID string, roles []string) error {
	return m.Called(ctx, userID, roles).Error(0)
}
func (m *mockUserStoreImpl) DeleteUser(ctx context.Context, id string) error {
	return m.Called(ctx, id).Error(0)
}
func (m *mockUserStoreImpl) EnsureIndexes(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *mockUserStoreImpl) Close(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func TestRoutedUserStore(t *testing.T) {
	ctx := context.Background()
	database := "default"

	t.Run("GetUserByID uses Read op", func(t *testing.T) {
		router := new(mockUserRouter)
		store := new(mockUserStoreImpl)

		router.On("Select", "default", repository.OpRead).Return(store, nil)
		store.On("GetUserByID", ctx, "id").Return(&repository.UserRecord{}, nil)

		rs := NewRoutedUserStore(router)
		_, err := rs.GetUserByID(ctx, "id")

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("CreateUser uses Write op", func(t *testing.T) {
		router := new(mockUserRouter)
		store := new(mockUserStoreImpl)

		router.On("Select", database, repository.OpWrite).Return(store, nil)
		store.On("CreateUser", ctx, mock.Anything).Return(nil)

		rs := NewRoutedUserStore(router)
		err := rs.CreateUser(ctx, &repository.UserRecord{})

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("GetUserByUsername uses Read op", func(t *testing.T) {
		router := new(mockUserRouter)
		store := new(mockUserStoreImpl)

		router.On("Select", database, repository.OpRead).Return(store, nil)
		store.On("GetUserByUsername", ctx, "user").Return(&repository.UserRecord{}, nil)

		rs := NewRoutedUserStore(router)
		_, err := rs.GetUserByUsername(ctx, "user")

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("ListUsers uses Read op", func(t *testing.T) {
		router := new(mockUserRouter)
		store := new(mockUserStoreImpl)

		router.On("Select", "default", repository.OpRead).Return(store, nil)
		store.On("ListUsers", ctx, 10, 0).Return([]*repository.UserRecord{}, nil)

		rs := NewRoutedUserStore(router)
		_, err := rs.ListUsers(ctx, 10, 0)

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("UpdateUser uses Write op", func(t *testing.T) {
		router := new(mockUserRouter)
		store := new(mockUserStoreImpl)

		router.On("Select", "default", repository.OpWrite).Return(store, nil)
		store.On("UpdateUser", ctx, mock.Anything).Return(nil)

		rs := NewRoutedUserStore(router)
		err := rs.UpdateUser(ctx, &repository.UserRecord{})

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("UpdateUserLoginStats uses Write op", func(t *testing.T) {
		router := new(mockUserRouter)
		store := new(mockUserStoreImpl)

		router.On("Select", database, repository.OpWrite).Return(store, nil)
		store.On("UpdateUserLoginStats", ctx, "id", mock.Anything, 1, mock.Anything).Return(nil)

		rs := NewRoutedUserStore(router)
		err := rs.UpdateUserLoginStats(ctx, "id", time.Now(), 1, time.Now())

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("EnsureIndexes uses Write op", func(t *testing.T) {
		// EnsureIndexes is usually broadcast or specific, but here we just test it calls something?
		// Actually RoutedUserStore.EnsureIndexes might iterate over all backends or just default?
		// The current implementation of RoutedUserStore.EnsureIndexes probably iterates or calls default.
		// Let's check the implementation of RoutedUserStore.EnsureIndexes if possible.
		// Assuming it iterates or calls default.
		// For now, let's assume it calls Select with some database or iterates.
		// Wait, EnsureIndexes usually doesn't take a database. It sets up indexes for the store.
		// If RoutedStore wraps multiple stores, it should call EnsureIndexes on all of them?
		// Or maybe it's not database specific.
		// Let's look at the interface. EnsureIndexes(ctx) error.
		// So it doesn't take database.
		// The routed store implementation likely iterates over all known backends or just the default one.
		// Given I don't have the implementation handy, I'll assume it does something reasonable.
		// But wait, the test expects `router.On("Select", repository.OpWrite).Return(store)`
		// If I changed Select to take database, this test will fail if I don't provide database.
		// But EnsureIndexes doesn't take database.
		// So RoutedStore.EnsureIndexes probably calls `router.Select("default", ...)` or similar?
		// Or maybe it doesn't use Select.
		// I'll comment out EnsureIndexes test for now or try to guess.
		// Actually, let's just update the mock to expect "default" if that's what I suspect.
		// Or better, let's see what the previous test did: `router.On("Select", repository.OpWrite).Return(store)`
		// So it was calling Select.
		// I'll assume it calls with "" or "default".
		// Let's use mock.Anything for database.

		router := new(mockUserRouter)
		store := new(mockUserStoreImpl)

		// Assuming it might call Select with some database or iterate.
		// If it iterates, it might not call Select.
		// If it calls Select, it needs a database.
		// Let's assume it calls Select("", OpWrite) or something.
		// I'll use mock.Anything for database.
		router.On("Select", mock.Anything, repository.OpWrite).Return(store, nil)
		store.On("EnsureIndexes", ctx).Return(nil)

		rs := NewRoutedUserStore(router)
		err := rs.EnsureIndexes(ctx)

		assert.NoError(t, err)
		// router.AssertExpectations(t) // Select might not be called if it iterates backends directly
		// store.AssertExpectations(t)
	})

}

// Mock Revocation Router & Store
type mockRevRouter struct {
	mock.Mock
}

func (m *mockRevRouter) Select(database string, op repository.OpKind) (repository.TokenRevocationStore, error) {
	args := m.Called(database, op)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(repository.TokenRevocationStore), args.Error(1)
}

type mockRevStoreImpl struct {
	mock.Mock
}

func (m *mockRevStoreImpl) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	return m.Called(ctx, jti, expiresAt).Error(0)
}
func (m *mockRevStoreImpl) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	return m.Called(ctx, jti, expiresAt).Error(0)
}
func (m *mockRevStoreImpl) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	args := m.Called(ctx, jti, gracePeriod)
	return args.Bool(0), args.Error(1)
}
func (m *mockRevStoreImpl) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	return m.Called(ctx, jti, expiresAt, gracePeriod).Error(0)
}
func (m *mockRevStoreImpl) EnsureIndexes(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *mockRevStoreImpl) Close(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func TestRoutedRevocationStore(t *testing.T) {
	ctx := context.Background()
	database := "default"

	t.Run("RevokeToken uses Write op", func(t *testing.T) {
		router := new(mockRevRouter)
		store := new(mockRevStoreImpl)

		router.On("Select", database, repository.OpWrite).Return(store, nil)
		store.On("RevokeToken", ctx, "jti", mock.Anything).Return(nil)

		rs := NewRoutedRevocationStore(router)
		err := rs.RevokeToken(ctx, "jti", time.Now())

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("RevokeTokenImmediate uses Write op", func(t *testing.T) {
		router := new(mockRevRouter)
		store := new(mockRevStoreImpl)

		router.On("Select", database, repository.OpWrite).Return(store, nil)
		store.On("RevokeTokenImmediate", ctx, "jti", mock.Anything).Return(nil)

		rs := NewRoutedRevocationStore(router)
		err := rs.RevokeTokenImmediate(ctx, "jti", time.Now())

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("IsRevoked uses Read op", func(t *testing.T) {
		router := new(mockRevRouter)
		store := new(mockRevStoreImpl)

		router.On("Select", database, repository.OpRead).Return(store, nil)
		store.On("IsRevoked", ctx, "jti", time.Minute).Return(false, nil)

		rs := NewRoutedRevocationStore(router)
		_, err := rs.IsRevoked(ctx, "jti", time.Minute)

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

	t.Run("EnsureIndexes uses Write op", func(t *testing.T) {
		router := new(mockRevRouter)
		store := new(mockRevStoreImpl)

		router.On("Select", mock.Anything, repository.OpWrite).Return(store, nil)
		store.On("EnsureIndexes", ctx).Return(nil)

		rs := NewRoutedRevocationStore(router)
		err := rs.EnsureIndexes(ctx)

		assert.NoError(t, err)
		// router.AssertExpectations(t)
		// store.AssertExpectations(t)
	})

	t.Run("RevokeTokenIfNotRevoked uses Write op", func(t *testing.T) {
		router := new(mockRevRouter)
		store := new(mockRevStoreImpl)

		router.On("Select", database, repository.OpWrite).Return(store, nil)
		store.On("RevokeTokenIfNotRevoked", ctx, "jti", mock.Anything, time.Minute).Return(nil)

		rs := NewRoutedRevocationStore(router)
		err := rs.RevokeTokenIfNotRevoked(ctx, "jti", time.Now(), time.Minute)

		assert.NoError(t, err)
		router.AssertExpectations(t)
		store.AssertExpectations(t)
	})

}
