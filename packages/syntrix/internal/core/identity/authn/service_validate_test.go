package authn

import (
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateToken(t *testing.T) {
	t.Parallel()
	mockStorage := new(MockStorage)
	cfg := config.AuthNConfig{
		PrivateKeyFile:  getTestKeyPath(t),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 7 * 24 * time.Hour,
		AuthCodeTTL:     2 * time.Minute,
	}
	svc, err := NewAuthService(cfg, mockStorage, mockStorage)
	require.NoError(t, err)
	authService := svc

	// Generate a token
	token, err := authService.GenerateSystemToken("test-service")
	require.NoError(t, err)

	// Validate valid token
	actor, err := authService.VerifyToken(token)
	assert.NoError(t, err)
	claims := actor.Claims()
	assert.Equal(t, "system:test-service", claims.Subject)

	// Validate invalid token
	_, err = authService.VerifyToken("invalid-token")
	assert.Error(t, err)
}

func TestValidatePasswordComplexity(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  error
	}{
		{
			name:     "valid password",
			password: "SecurePass123!",
			wantErr:  nil,
		},
		{
			name:     "too short",
			password: "Short1!",
			wantErr:  ErrPasswordTooShort,
		},
		{
			name:     "no uppercase",
			password: "securepass123!",
			wantErr:  ErrPasswordNoUppercase,
		},
		{
			name:     "no lowercase",
			password: "SECUREPASS123!",
			wantErr:  ErrPasswordNoLowercase,
		},
		{
			name:     "no digit",
			password: "SecurePassword!",
			wantErr:  ErrPasswordNoDigit,
		},
		{
			name:     "no special char",
			password: "SecurePass1234",
			wantErr:  ErrPasswordNoSpecial,
		},
		{
			name:     "only lowercase",
			password: "onlylowercasepassword",
			wantErr:  ErrPasswordNoUppercase,
		},
		{
			name:     "complex valid password",
			password: "MyP@ssw0rd!123",
			wantErr:  nil,
		},
		{
			name:     "password with spaces",
			password: "My Password 1!",
			wantErr:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePasswordComplexity(tt.password)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
