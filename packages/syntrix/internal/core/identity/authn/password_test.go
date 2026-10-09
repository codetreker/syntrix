package authn

import (
	"testing"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/stretchr/testify/assert"
)

func TestPasswordValidator_Validate(t *testing.T) {
	policy := config.PasswordPolicyConfig{
		MinLength:        12,
		RequireUppercase: true,
		RequireLowercase: true,
		RequireDigit:     true,
		RequireSpecial:   true,
	}
	validator := NewPasswordValidator(policy)

	tests := []struct {
		name     string
		password string
		wantErr  error
	}{
		{
			name:     "Valid password",
			password: "SecurePass123!",
			wantErr:  nil,
		},
		{
			name:     "Too short",
			password: "Abc123!",
			wantErr:  ErrPasswordTooShort,
		},
		{
			name:     "No uppercase",
			password: "securepass123!",
			wantErr:  ErrPasswordNoUppercase,
		},
		{
			name:     "No lowercase",
			password: "SECUREPASS123!",
			wantErr:  ErrPasswordNoLowercase,
		},
		{
			name:     "No digit",
			password: "SecurePassword!",
			wantErr:  ErrPasswordNoDigit,
		},
		{
			name:     "No special character",
			password: "SecurePass1234",
			wantErr:  ErrPasswordNoSpecial,
		},
		{
			name:     "Unicode special character",
			password: "SecurePass123™",
			wantErr:  nil,
		},
		{
			name:     "Space as special character",
			password: "Secure Pass 123",
			wantErr:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.password)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPasswordValidator_DisabledChecks(t *testing.T) {
	// Test with all checks disabled except min length
	policy := config.PasswordPolicyConfig{
		MinLength:        8,
		RequireUppercase: false,
		RequireLowercase: false,
		RequireDigit:     false,
		RequireSpecial:   false,
	}
	validator := NewPasswordValidator(policy)

	// Simple password should pass
	err := validator.Validate("simplepassword")
	assert.NoError(t, err)

	// Too short should still fail
	err = validator.Validate("short")
	assert.ErrorIs(t, err, ErrPasswordTooShort)
}

func TestContainsUppercase(t *testing.T) {
	assert.True(t, containsUppercase("Hello"))
	assert.True(t, containsUppercase("HELLO"))
	assert.False(t, containsUppercase("hello"))
	assert.False(t, containsUppercase("12345"))
}

func TestContainsLowercase(t *testing.T) {
	assert.True(t, containsLowercase("Hello"))
	assert.True(t, containsLowercase("hello"))
	assert.False(t, containsLowercase("HELLO"))
	assert.False(t, containsLowercase("12345"))
}

func TestContainsDigit(t *testing.T) {
	assert.True(t, containsDigit("Hello1"))
	assert.True(t, containsDigit("12345"))
	assert.False(t, containsDigit("Hello"))
	assert.False(t, containsDigit("!@#$%"))
}

func TestContainsSpecial(t *testing.T) {
	assert.True(t, containsSpecial("Hello!"))
	assert.True(t, containsSpecial("Hello World"))
	assert.True(t, containsSpecial("!@#$%"))
	assert.False(t, containsSpecial("Hello"))
	assert.False(t, containsSpecial("Hello123"))
}
