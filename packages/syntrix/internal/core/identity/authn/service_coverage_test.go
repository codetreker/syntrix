package authn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSignUp_Coverage(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name        string
		req         SignupRequest
		mockSetup   func(*MockStorage)
		expectError bool
		errorMsg    string
	}

	tests := []testCase{
		{
			name: "User Already Exists",
			req: SignupRequest{
				Username: "existing",
				Password: "Password12345!",
			},
			mockSetup: func(m *MockStorage) {
				m.On("GetUserByUsername", mock.Anything, "existing").Return(&User{}, nil)
			},
			expectError: true,
			errorMsg:    "user already exists",
		},
		{
			name: "Storage Error (Check User)",
			req: SignupRequest{
				Username: "error",
				Password: "Password12345!",
			},
			mockSetup: func(m *MockStorage) {
				m.On("GetUserByUsername", mock.Anything, "error").Return(nil, errors.New("db error"))
			},
			expectError: true,
			errorMsg:    "db error",
		},
		{
			name: "Password Too Short",
			req: SignupRequest{
				Username: "short",
				Password: "short",
			},
			mockSetup: func(m *MockStorage) {
				m.On("GetUserByUsername", mock.Anything, "short").Return(nil, ErrUserNotFound)
			},
			expectError: true,
			errorMsg:    "password must be at least 12 characters",
		},
		{
			name: "Storage Error (Create User)",
			req: SignupRequest{
				Username: "create_error",
				Password: "Password12345!",
			},
			mockSetup: func(m *MockStorage) {
				m.On("GetUserByUsername", mock.Anything, "create_error").Return(nil, ErrUserNotFound)
				m.On("CreateUser", mock.Anything, mock.Anything).Return(errors.New("create failed"))
			},
			expectError: true,
			errorMsg:    "create failed",
		},
		{
			name: "Admin Role Assignment",
			req: SignupRequest{
				Username: "syntrix",
				Password: "Password12345!",
			},
			mockSetup: func(m *MockStorage) {
				m.On("GetUserByUsername", mock.Anything, "syntrix").Return(nil, ErrUserNotFound)
				m.On("CreateUser", mock.Anything, mock.MatchedBy(func(u *User) bool {
					for _, r := range u.Roles {
						if r == "admin" {
							return true
						}
					}
					return false
				})).Return(nil)
			},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mockStorage := new(MockStorage)
			cfg := config.AuthNConfig{
				PrivateKeyFile:  getTestKeyPath(t),
				AccessTokenTTL:  15 * time.Minute,
				RefreshTokenTTL: 7 * 24 * time.Hour,
				AuthCodeTTL:     2 * time.Minute,
				AdminUsername:   "syntrix", // Configure admin username for Admin Role Assignment test
				PasswordPolicy: config.PasswordPolicyConfig{
					MinLength:        12,
					RequireUppercase: true,
					RequireLowercase: true,
					RequireDigit:     true,
					RequireSpecial:   true,
				},
			}
			svc, err := newTestAccountService(cfg, mockStorage, mockStorage)
			require.NoError(t, err)
			authService := svc

			if tc.mockSetup != nil {
				tc.mockSetup(mockStorage)
			}

			tokenPair, err := authService.SignUp(context.Background(), tc.req)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorMsg != "" {
					assert.Contains(t, err.Error(), tc.errorMsg)
				}
				assert.Nil(t, tokenPair)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, tokenPair)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestRefresh_Coverage(t *testing.T) {
	t.Parallel()
	cfg := config.AuthNConfig{
		PrivateKeyFile:  filepath.Join(t.TempDir(), "key.pem"),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 24 * time.Hour,
	}

	tests := []struct {
		name        string
		setupToken  func(t *testing.T, svc *accountService, m *MockStorage) string
		mockSetup   func(*MockStorage)
		expectError bool
		errorIs     error
	}{
		{
			name: "Revoked Token",
			setupToken: func(t *testing.T, svc *accountService, m *MockStorage) string {
				m.On("GetUserByUsername", mock.Anything, "revoked").Return(nil, ErrUserNotFound).Once()
				m.On("CreateUser", mock.Anything, mock.Anything).Return(nil).Once()
				resp, err := svc.SignUp(context.Background(), SignupRequest{
					Username: "revoked", Password: "Password12345!",
				})
				require.NoError(t, err)
				return resp.RefreshToken
			},
			mockSetup: func(m *MockStorage) {
				// Token already revoked - RevokeTokenIfNotRevoked returns ErrTokenAlreadyRevoked
				m.On("RevokeTokenIfNotRevoked", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(storage.ErrTokenAlreadyRevoked)
			},
			expectError: true,
			errorIs:     ErrInvalidToken,
		},
		{
			name: "Revocation Check Error",
			setupToken: func(t *testing.T, svc *accountService, m *MockStorage) string {
				m.On("GetUserByUsername", mock.Anything, "reverr").Return(nil, ErrUserNotFound).Once()
				m.On("CreateUser", mock.Anything, mock.Anything).Return(nil).Once()
				resp, err := svc.SignUp(context.Background(), SignupRequest{
					Username: "reverr", Password: "Password12345!",
				})
				require.NoError(t, err)
				return resp.RefreshToken
			},
			mockSetup: func(m *MockStorage) {
				// Database error during atomic revocation
				m.On("RevokeTokenIfNotRevoked", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errors.New("db error"))
			},
			expectError: true,
		},
		{
			name: "User Not Found",
			setupToken: func(t *testing.T, svc *accountService, m *MockStorage) string {
				m.On("GetUserByUsername", mock.Anything, "missing").Return(nil, ErrUserNotFound).Once()
				m.On("CreateUser", mock.Anything, mock.Anything).Return(nil).Once()
				resp, err := svc.SignUp(context.Background(), SignupRequest{
					Username: "missing", Password: "Password12345!",
				})
				require.NoError(t, err)
				return resp.RefreshToken
			},
			mockSetup: func(m *MockStorage) {
				m.On("RevokeTokenIfNotRevoked", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
				m.On("GetUserByID", mock.Anything, mock.Anything).Return(nil, errors.New("not found"))
			},
			expectError: true,
			errorIs:     ErrInvalidToken,
		},
		{
			name: "User Disabled",
			setupToken: func(t *testing.T, svc *accountService, m *MockStorage) string {
				m.On("GetUserByUsername", mock.Anything, "disabled").Return(nil, ErrUserNotFound).Once()
				m.On("CreateUser", mock.Anything, mock.Anything).Return(nil).Once()
				resp, err := svc.SignUp(context.Background(), SignupRequest{
					Username: "disabled", Password: "Password12345!",
				})
				require.NoError(t, err)
				return resp.RefreshToken
			},
			mockSetup: func(m *MockStorage) {
				m.On("RevokeTokenIfNotRevoked", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
				m.On("GetUserByID", mock.Anything, mock.Anything).Return(&User{
					ID: "user-id", Username: "disabled", Disabled: true,
				}, nil)
			},
			expectError: true,
			errorIs:     ErrAccountDisabled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockStorage := new(MockStorage)
			svc, err := newTestAccountService(cfg, mockStorage, mockStorage)
			require.NoError(t, err)

			token := tt.setupToken(t, svc, mockStorage)

			if tt.mockSetup != nil {
				tt.mockSetup(mockStorage)
			}

			resp, err := svc.Refresh(context.Background(), RefreshRequest{RefreshToken: token})

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorIs != nil {
					assert.ErrorIs(t, err, tt.errorIs)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
			}
		})
	}
}

func TestNewServices_Coverage(t *testing.T) {
	// Force invalid key path by using an existing file as the parent directory
	tmpDir := t.TempDir()
	parentFile := filepath.Join(tmpDir, "parent-file")
	require.NoError(t, os.WriteFile(parentFile, []byte("noop"), 0600))

	cfg := config.AuthNConfig{
		PrivateKeyFile: filepath.Join(parentFile, "key.pem"),
	}

	svc, err := newTestAccountService(cfg, nil, nil)
	assert.Error(t, err)
	assert.Nil(t, svc)
}

func TestAuthService_Logout_Coverage(t *testing.T) {
	t.Parallel()
	// Setup service with valid key (generate one)
	tmpDir := t.TempDir()
	keyFile := filepath.Join(tmpDir, "test.pem")

	cfg := config.AuthNConfig{
		PrivateKeyFile: keyFile,
	}

	svc, err := newTestAccountService(cfg, nil, nil)
	assert.NoError(t, err)

	// Case 1: Invalid token
	err = svc.Logout(context.Background(), "invalid-token")
	assert.Equal(t, ErrInvalidToken, err)
}
