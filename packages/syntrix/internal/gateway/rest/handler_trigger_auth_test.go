package rest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/authn"
	identity_config "github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockAuthStorage implements auth.StorageInterface
type MockAuthStorage struct {
	mock.Mock
}

func (m *MockAuthStorage) CreateUser(ctx context.Context, user *storage.User) error {
	return m.Called(ctx, user).Error(0)
}
func (m *MockAuthStorage) GetUserByUsername(ctx context.Context, username string) (*storage.User, error) {
	args := m.Called(ctx, username)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.User), args.Error(1)
}
func (m *MockAuthStorage) GetUserByID(ctx context.Context, id string) (*storage.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.User), args.Error(1)
}
func (m *MockAuthStorage) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	return m.Called(ctx, id, lastLogin, attempts, lockoutUntil).Error(0)
}
func (m *MockAuthStorage) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	return m.Called(ctx, jti, expiresAt).Error(0)
}
func (m *MockAuthStorage) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	return m.Called(ctx, jti, expiresAt).Error(0)
}
func (m *MockAuthStorage) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	args := m.Called(ctx, jti, gracePeriod)
	return args.Bool(0), args.Error(1)
}
func (m *MockAuthStorage) ListUsers(ctx context.Context, limit int, offset int) ([]*storage.User, error) {
	args := m.Called(ctx, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*storage.User), args.Error(1)
}

func (m *MockAuthStorage) UpdateUser(ctx context.Context, user *storage.User) error {
	return m.Called(ctx, user).Error(0)
}
func (m *MockAuthStorage) EnsureIndexes(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *MockAuthStorage) Close(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *MockAuthStorage) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	return m.Called(ctx, jti, expiresAt, gracePeriod).Error(0)
}
func TestTriggerAuth(t *testing.T) {
	// Setup Auth Service
	mockStorage := new(MockAuthStorage)
	authService, verifier, issuer, _ := authn.NewServices(identity_config.AuthNConfig{
		PrivateKeyFile:  filepath.Join(t.TempDir(), "key.pem"),
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: time.Hour,
		AuthCodeTTL:     time.Minute,
	}, mockStorage, mockStorage)

	// Setup Server
	mockEngine := new(MockQueryService)
	handler, err := NewHandler(mockEngine, authService, verifier, new(AllowAllAuthzService))
	require.NoError(t, err)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	server := &TestServer{Handler: handler, mux: mux}

	mockStorage.On("GetUserByUsername", mock.Anything, "user1").Return(nil, identity.ErrUserNotFound)
	mockStorage.On("CreateUser", mock.Anything, mock.Anything).Return(nil)

	userToken, err := authService.SignUp(context.Background(), identity.SignupRequest{Username: "user1", Password: "Password12345!"})
	if err != nil {
		t.Fatalf("Failed to sign up: %v", err)
	}

	systemToken, _ := issuer.GenerateSystemToken("trigger-worker")

	t.Run("Reject No Token", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/trigger/v1/databases/default/get", nil)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("Reject User Token", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/trigger/v1/databases/default/get", nil)
		req.Header.Set("Authorization", "Bearer "+userToken.AccessToken)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("Accept System Token", func(t *testing.T) {
		// Mock Engine call because auth should pass
		mockEngine.On("GetDocument", mock.Anything, "default", "test/doc").Return(model.Document{"id": "doc1", "collection": "test", "version": int64(1)}, nil).Once()

		reqBody := `{"paths": ["test/doc"]}`
		req := httptest.NewRequest("POST", "/trigger/v1/databases/default/get", bytes.NewBufferString(reqBody))
		req.Header.Set("Authorization", "Bearer "+systemToken)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}
