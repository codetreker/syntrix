package authn

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/identity/config"
	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockUserStore
type MockUserStore struct {
	mock.Mock
}

func (m *MockUserStore) CreateUser(ctx context.Context, user *repository.UserRecord) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *MockUserStore) GetUserByUsername(ctx context.Context, username string) (*repository.UserRecord, error) {
	args := m.Called(ctx, username)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*repository.UserRecord), args.Error(1)
}

func (m *MockUserStore) GetUserByID(ctx context.Context, id string) (*repository.UserRecord, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*repository.UserRecord), args.Error(1)
}

func (m *MockUserStore) ListUsers(ctx context.Context, limit int, offset int) ([]*repository.UserRecord, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*repository.UserRecord), args.Error(1)
}

func (m *MockUserStore) UpdateUser(ctx context.Context, user *repository.UserRecord) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *MockUserStore) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	args := m.Called(ctx, id, lastLogin, attempts, lockoutUntil)
	return args.Error(0)
}

func (m *MockUserStore) EnsureIndexes(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockUserStore) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// MockTokenRevocationStore
type MockTokenRevocationStore struct {
	mock.Mock
}

func (m *MockTokenRevocationStore) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	args := m.Called(ctx, jti, expiresAt)
	return args.Error(0)
}

func (m *MockTokenRevocationStore) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	args := m.Called(ctx, jti, expiresAt)
	return args.Error(0)
}

func (m *MockTokenRevocationStore) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	args := m.Called(ctx, jti, gracePeriod)
	return args.Bool(0), args.Error(1)
}

func (m *MockTokenRevocationStore) EnsureIndexes(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockTokenRevocationStore) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockTokenRevocationStore) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	args := m.Called(ctx, jti, expiresAt, gracePeriod)
	return args.Error(0)
}

func TestNewServices(t *testing.T) {
	// Create a temporary file for the private key
	tmpFile := t.TempDir() + "/private.pem"

	cfg := config.AuthNConfig{
		PrivateKeyFile:  tmpFile,
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: time.Hour * 24,
	}

	mockUsers := new(MockUserStore)
	mockRevocations := new(MockTokenRevocationStore)

	accounts, verifier, issuer, err := NewServices(cfg, mockUsers, mockRevocations)
	assert.NoError(t, err)
	assert.NotNil(t, accounts)
	assert.NotNil(t, verifier)
	assert.NotNil(t, issuer)

	// Verify that the private key file was created
	_, err = os.Stat(tmpFile)
	assert.NoError(t, err)
}
