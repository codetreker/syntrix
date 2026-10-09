package identity

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockUserStore
type MockUserStore struct {
	mock.Mock
}

func (m *MockUserStore) CreateUser(ctx context.Context, user *storage.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *MockUserStore) GetUserByUsername(ctx context.Context, username string) (*storage.User, error) {
	args := m.Called(ctx, username)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.User), args.Error(1)
}

func (m *MockUserStore) GetUserByID(ctx context.Context, id string) (*storage.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.User), args.Error(1)
}

func (m *MockUserStore) ListUsers(ctx context.Context, limit int, offset int) ([]*storage.User, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*storage.User), args.Error(1)
}

func (m *MockUserStore) UpdateUser(ctx context.Context, user *storage.User) error {
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

// MockQueryService
type MockQueryService struct {
	mock.Mock
}

func (m *MockQueryService) GetDocument(ctx context.Context, database string, path string) (model.Document, error) {
	args := m.Called(ctx, database, path)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockQueryService) CreateDocument(ctx context.Context, database string, doc model.Document) error {
	args := m.Called(ctx, database, doc)
	return args.Error(0)
}

func (m *MockQueryService) ReplaceDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	args := m.Called(ctx, database, data, pred)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockQueryService) PatchDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	args := m.Called(ctx, database, data, pred)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockQueryService) DeleteDocument(ctx context.Context, database string, path string, pred model.Filters) error {
	args := m.Called(ctx, database, path, pred)
	return args.Error(0)
}

func (m *MockQueryService) ExecuteQuery(ctx context.Context, database string, q model.Query) ([]model.Document, error) {
	args := m.Called(ctx, database, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]model.Document), args.Error(1)
}

func (m *MockQueryService) WatchCollection(ctx context.Context, database string, collection string) (<-chan storage.Event, error) {
	args := m.Called(ctx, database, collection)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(<-chan storage.Event), args.Error(1)
}

func (m *MockQueryService) Pull(ctx context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	args := m.Called(ctx, database, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.ReplicationPullResponse), args.Error(1)
}

func (m *MockQueryService) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	args := m.Called(ctx, database, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.ReplicationPushResponse), args.Error(1)
}

func TestNewAuthN(t *testing.T) {
	// Create a temporary file for the private key
	tmpFile := t.TempDir() + "/private.pem"

	cfg := config.AuthNConfig{
		PrivateKeyFile:  tmpFile,
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: time.Hour * 24,
	}

	mockUsers := new(MockUserStore)
	mockRevocations := new(MockTokenRevocationStore)

	authn, err := NewAuthN(cfg, mockUsers, mockRevocations)
	assert.NoError(t, err)
	assert.NotNil(t, authn)

	// Verify that the private key file was created
	_, err = os.Stat(tmpFile)
	assert.NoError(t, err)
}

func TestNewAuthZ(t *testing.T) {
	cfg := config.AuthZConfig{}
	mockQuery := new(MockQueryService)

	authz, err := NewAuthZ(cfg, mockQuery)
	assert.NoError(t, err)
	assert.NotNil(t, authz)
}
