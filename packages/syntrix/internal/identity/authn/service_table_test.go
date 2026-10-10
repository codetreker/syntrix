package authn

import (
	"context"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/identity/config"
	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSignUp_TableDriven(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name        string
		req         SignupRequest
		mockSetup   func(*MockStorage)
		expectError bool
	}

	tests := []testCase{
		{
			name: "Success",
			req: SignupRequest{
				Username: "newuser",
				Password: "Password12345!",
			},
			mockSetup: func(m *MockStorage) {
				m.On("GetUserByUsername", mock.Anything, "newuser").Return(nil, ErrUserNotFound)
				m.On("CreateUser", mock.Anything, mock.Anything).Return(nil)
			},
			expectError: false,
		},
		// Add more cases: UserExists, StorageError, etc.
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mockStorage := new(MockStorage)
			cfg := config.AuthNConfig{
				PrivateKeyFile:  getTestKeyPath(t),
				AccessTokenTTL:  15 * time.Minute,
				RefreshTokenTTL: 7 * 24 * time.Hour,
				AuthCodeTTL:     2 * time.Minute,
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
				assert.Nil(t, tokenPair)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, tokenPair)
				assert.NotEmpty(t, tokenPair.AccessToken)
				assert.NotEmpty(t, tokenPair.RefreshToken)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestSignIn_TableDriven(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name        string
		req         LoginRequest
		mockSetup   func(*MockStorage)
		expectError bool
		errorIs     error
	}

	tests := []testCase{
		{
			name: "Success",
			req: LoginRequest{
				Username: "existinguser",
				Password: "Password123!",
			},
			mockSetup: func(m *MockStorage) {
				hash, algo, _ := HashPassword("Password123!")
				user := &repository.UserRecord{
					ID:           "user-id",
					Username:     "existinguser",
					PasswordHash: hash,
					PasswordAlgo: algo,
				}
				m.On("GetUserByUsername", mock.Anything, "existinguser").Return(user, nil)
				m.On("UpdateUserLoginStats", mock.Anything, "user-id", mock.Anything, 0, mock.Anything).Return(nil)
			},
			expectError: false,
		},
		{
			name: "repository.UserRecord Not Found",
			req: LoginRequest{
				Username: "nonexistent",
				Password: "Password12345!",
			},
			mockSetup: func(m *MockStorage) {
				m.On("GetUserByUsername", mock.Anything, "nonexistent").Return(nil, ErrUserNotFound)
			},
			expectError: true,
			errorIs:     ErrUserNotFound,
		},
		{
			name: "Wrong Password",
			req: LoginRequest{
				Username: "existinguser",
				Password: "wrongpassword",
			},
			mockSetup: func(m *MockStorage) {
				hash, algo, _ := HashPassword("Password123!")
				user := &repository.UserRecord{
					ID:           "user-id",
					Username:     "existinguser",
					PasswordHash: hash,
					PasswordAlgo: algo,
				}
				m.On("GetUserByUsername", mock.Anything, "existinguser").Return(user, nil)
				// Expect login stats update (failed attempt)
				m.On("UpdateUserLoginStats", mock.Anything, "user-id", mock.Anything, 1, mock.Anything).Return(nil)
			},
			expectError: true,
			errorIs:     ErrInvalidCredentials,
		},
		{
			name: "Wrong Password Lockout",
			req: LoginRequest{
				Username: "existinguser",
				Password: "wrongpassword",
			},
			mockSetup: func(m *MockStorage) {
				hash, algo, _ := HashPassword("Password123!")
				user := &repository.UserRecord{
					ID:            "user-id",
					Username:      "existinguser",
					PasswordHash:  hash,
					PasswordAlgo:  algo,
					LoginAttempts: 9,
				}
				m.On("GetUserByUsername", mock.Anything, "existinguser").Return(user, nil)
				// Expect login stats update (failed attempt, lockout)
				m.On("UpdateUserLoginStats", mock.Anything, "user-id", mock.Anything, 10, mock.MatchedBy(func(lockoutUntil time.Time) bool {
					return lockoutUntil.After(time.Now())
				})).Return(nil)
			},
			expectError: true,
			errorIs:     ErrInvalidCredentials,
		},
		{
			name: "Locked Out",
			req: LoginRequest{
				Username: "locked",
				Password: "Password123!",
			},
			mockSetup: func(m *MockStorage) {
				user := &repository.UserRecord{
					ID:           "user-id",
					Username:     "locked",
					LockoutUntil: time.Now().Add(time.Hour),
				}
				m.On("GetUserByUsername", mock.Anything, "locked").Return(user, nil)
			},
			expectError: true,
			errorIs:     ErrAccountLocked,
		},
		{
			name: "Disabled",
			req: LoginRequest{
				Username: "disabled",
				Password: "Password123!",
			},
			mockSetup: func(m *MockStorage) {
				hash, algo, _ := HashPassword("Password123!")
				user := &repository.UserRecord{
					ID:           "user-id",
					Username:     "disabled",
					PasswordHash: hash,
					PasswordAlgo: algo,
					Disabled:     true,
				}
				m.On("GetUserByUsername", mock.Anything, "disabled").Return(user, nil)
			},
			expectError: true,
			errorIs:     ErrAccountDisabled,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mockStorage := new(MockStorage)
			cfg := config.AuthNConfig{
				PrivateKeyFile:  getTestKeyPath(t),
				AccessTokenTTL:  15 * time.Minute,
				RefreshTokenTTL: 7 * 24 * time.Hour,
				AuthCodeTTL:     2 * time.Minute,
			}
			svc, err := newTestAccountService(cfg, mockStorage, mockStorage)
			require.NoError(t, err)
			authService := svc

			if tc.mockSetup != nil {
				tc.mockSetup(mockStorage)
			}

			tokenPair, err := authService.SignIn(context.Background(), tc.req)

			if tc.expectError {
				assert.Error(t, err)
				assert.Nil(t, tokenPair)
				if tc.errorIs != nil {
					assert.Equal(t, tc.errorIs, err)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, tokenPair)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestRefresh_TableDriven(t *testing.T) {
	t.Parallel()
	cfg := config.AuthNConfig{
		PrivateKeyFile:  getTestKeyPath(t),
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
			name: "Success",
			setupToken: func(t *testing.T, svc *accountService, m *MockStorage) string {
				// Mock SignUp to get a token
				m.On("GetUserByUsername", mock.Anything, "refreshuser").Return(nil, ErrUserNotFound).Once()
				m.On("CreateUser", mock.Anything, mock.Anything).Return(nil).Once()
				resp, err := svc.SignUp(context.Background(), SignupRequest{
					Username: "refreshuser", Password: "Password12345!",
				})
				require.NoError(t, err)
				return resp.RefreshToken
			},
			mockSetup: func(m *MockStorage) {
				m.On("RevokeTokenIfNotRevoked", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
				m.On("GetUserByID", mock.Anything, mock.Anything).Return(&repository.UserRecord{
					ID: "user-id", Username: "refreshuser", Disabled: false,
				}, nil)
			},
			expectError: false,
		},
		{
			name: "Invalid Token",
			setupToken: func(t *testing.T, svc *accountService, m *MockStorage) string {
				return "invalid-token"
			},
			mockSetup:   func(m *MockStorage) {},
			expectError: true,
			errorIs:     ErrInvalidToken,
		},
	}

	for _, tt := range tests {
		tt := tt
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
				assert.NotEmpty(t, resp.AccessToken)
			}
		})
	}
}

func TestLogout_TableDriven(t *testing.T) {
	t.Parallel()
	cfg := config.AuthNConfig{
		PrivateKeyFile:  getTestKeyPath(t),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 24 * time.Hour,
	}

	tests := []struct {
		name        string
		setupToken  func(t *testing.T, svc *accountService, m *MockStorage) string
		mockSetup   func(*MockStorage)
		expectError bool
	}{
		{
			name: "Success",
			setupToken: func(t *testing.T, svc *accountService, m *MockStorage) string {
				m.On("GetUserByUsername", mock.Anything, "logoutuser").Return(nil, ErrUserNotFound).Once()
				m.On("CreateUser", mock.Anything, mock.Anything).Return(nil).Once()
				resp, err := svc.SignUp(context.Background(), SignupRequest{
					Username: "logoutuser", Password: "Password12345!",
				})
				require.NoError(t, err)
				return resp.AccessToken
			},
			mockSetup: func(m *MockStorage) {
				m.On("RevokeTokenImmediate", mock.Anything, mock.Anything, mock.Anything).Return(nil)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mockStorage := new(MockStorage)
			svc, err := newTestAccountService(cfg, mockStorage, mockStorage)
			require.NoError(t, err)

			token := tt.setupToken(t, svc, mockStorage)

			if tt.mockSetup != nil {
				tt.mockSetup(mockStorage)
			}

			err = svc.Logout(context.Background(), token)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
