package authn

import (
	"errors"
	"unicode"

	"github.com/codetreker/syntrix/internal/identity/config"
)

// Password validation errors
var (
	ErrPasswordTooShort    = errors.New("password must be at least 12 characters")
	ErrPasswordNoUppercase = errors.New("password must contain at least one uppercase letter")
	ErrPasswordNoLowercase = errors.New("password must contain at least one lowercase letter")
	ErrPasswordNoDigit     = errors.New("password must contain at least one digit")
	ErrPasswordNoSpecial   = errors.New("password must contain at least one special character")
)

// PasswordValidator validates passwords against a policy.
type PasswordValidator struct {
	policy config.PasswordPolicyConfig
}

// NewPasswordValidator creates a new password validator with the given policy.
func NewPasswordValidator(policy config.PasswordPolicyConfig) *PasswordValidator {
	return &PasswordValidator{policy: policy}
}

// Validate checks if the password meets all policy requirements.
func (v *PasswordValidator) Validate(password string) error {
	if len(password) < v.policy.MinLength {
		return ErrPasswordTooShort
	}

	if v.policy.RequireUppercase && !containsUppercase(password) {
		return ErrPasswordNoUppercase
	}

	if v.policy.RequireLowercase && !containsLowercase(password) {
		return ErrPasswordNoLowercase
	}

	if v.policy.RequireDigit && !containsDigit(password) {
		return ErrPasswordNoDigit
	}

	if v.policy.RequireSpecial && !containsSpecial(password) {
		return ErrPasswordNoSpecial
	}

	return nil
}

func containsUppercase(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func containsLowercase(s string) bool {
	for _, r := range s {
		if unicode.IsLower(r) {
			return true
		}
	}
	return false
}

func containsDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func containsSpecial(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// ValidatePasswordComplexity validates that a password meets security requirements
// using default strict policy.
//
// Deprecated: This function uses hardcoded policy values. Use PasswordValidator
// with configured policy instead. This function is kept for backward compatibility
// but will be removed in a future version.
func ValidatePasswordComplexity(password string) error {
	validator := NewPasswordValidator(config.PasswordPolicyConfig{
		MinLength:        12,
		RequireUppercase: true,
		RequireLowercase: true,
		RequireDigit:     true,
		RequireSpecial:   true,
	})
	return validator.Validate(password)
}
