package authn

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestValidateToken_ErrorPaths(t *testing.T) {
	t.Parallel()
	mockStorage := new(MockStorage)
	cfg := config.AuthNConfig{
		PrivateKeyFile: getTestKeyPath(t),
		AccessTokenTTL: time.Minute,
	}
	svc, err := newTestAccountService(cfg, mockStorage, mockStorage)
	require.NoError(t, err)
	authService := svc

	t.Run("Invalid Token Format", func(t *testing.T) {
		t.Parallel()
		_, err := authService.verifier.VerifyToken("invalid-token-string")
		assert.Error(t, err)
	})

	t.Run("Token Signed with Wrong Method", func(t *testing.T) {
		t.Parallel()
		// Create a token signed with HMAC instead of RSA
		token := jwt.New(jwt.SigningMethodHS256)
		tokenString, err := token.SignedString([]byte("secret"))
		require.NoError(t, err)

		_, err = authService.verifier.VerifyToken(tokenString)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected signing method")
	})
}

func TestSignIn_ErrorPaths(t *testing.T) {
	t.Parallel()

	mockStorage := new(MockStorage)
	cfg := config.AuthNConfig{
		PrivateKeyFile: getTestKeyPath(t),
	}
	svc, err := newTestAccountService(cfg, mockStorage, mockStorage)
	require.NoError(t, err)

	t.Run("VerifyPassword Error", func(t *testing.T) {
		t.Parallel()
		// Setup user with invalid hash format
		user := &User{
			ID:           "u1",
			Username:     "user",
			PasswordHash: "invalid-hash",
			PasswordAlgo: "bcrypt",
		}
		mockStorage.On("GetUserByUsername", mock.Anything, "user").Return(user, nil).Once()

		_, err := svc.SignIn(context.Background(), LoginRequest{
			Username: "user",
			Password: "password",
		})
		assert.Error(t, err)
	})
}

func TestRefresh_ErrorPaths_Extended(t *testing.T) {
	t.Parallel()
	mockStorage := new(MockStorage)
	cfg := config.AuthNConfig{
		PrivateKeyFile:  getTestKeyPath(t),
		AccessTokenTTL:  time.Minute,
		RefreshTokenTTL: time.Hour,
	}
	svc, err := newTestAccountService(cfg, mockStorage, mockStorage)
	require.NoError(t, err)

	// Helper to get a valid refresh token
	getRefreshToken := func() string {
		mockStorage.On("GetUserByUsername", mock.Anything, "user").Return(nil, ErrUserNotFound).Once()
		mockStorage.On("CreateUser", mock.Anything, mock.Anything).Return(nil).Once()
		pair, err := svc.SignUp(context.Background(), SignupRequest{
			Username: "user", Password: "Password123456!",
		})
		require.NoError(t, err)
		return pair.RefreshToken
	}

	t.Run("RevokeTokenIfNotRevoked Error", func(t *testing.T) {
		t.Parallel()
		token := getRefreshToken()

		// Mock failed atomic revocation
		mockStorage.On("RevokeTokenIfNotRevoked", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errors.New("revoke failed")).Once()

		_, err := svc.Refresh(context.Background(), RefreshRequest{RefreshToken: token})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "revoke failed")
	})
}
