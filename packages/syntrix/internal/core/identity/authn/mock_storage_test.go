package authn

import (
	"context"
	"time"

	"github.com/stretchr/testify/mock"
)

type MockStorage struct {
	mock.Mock
}

func (m *MockStorage) CreateUser(ctx context.Context, user *User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *MockStorage) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	args := m.Called(ctx, username)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*User), args.Error(1)
}

func (m *MockStorage) GetUserByID(ctx context.Context, id string) (*User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*User), args.Error(1)
}

func (m *MockStorage) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	args := m.Called(ctx, id, lastLogin, attempts, lockoutUntil)
	return args.Error(0)
}

func (m *MockStorage) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	args := m.Called(ctx, jti, expiresAt)
	return args.Error(0)
}

func (m *MockStorage) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	args := m.Called(ctx, jti, expiresAt)
	return args.Error(0)
}

func (m *MockStorage) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	args := m.Called(ctx, jti, gracePeriod)
	return args.Bool(0), args.Error(1)
}

func (m *MockStorage) ListUsers(ctx context.Context, limit int, offset int) ([]*User, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*User), args.Error(1)
}

func (m *MockStorage) UpdateUser(ctx context.Context, user *User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *MockStorage) EnsureIndexes(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockStorage) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockStorage) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	args := m.Called(ctx, jti, expiresAt, gracePeriod)
	return args.Error(0)
}
