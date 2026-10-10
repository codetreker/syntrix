package authn

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/google/uuid"
)

type UserStore = storage.UserStore
type TokenRevocationStore = storage.TokenRevocationStore

type AuthService struct {
	users             UserStore
	revocations       TokenRevocationStore
	tokenService      *TokenService
	verifier          *identity.Verifier
	passwordValidator *PasswordValidator
	adminUsername     string // Configurable admin username
}

func NewAuthService(cfg config.AuthNConfig, users UserStore, revocations TokenRevocationStore) (*AuthService, error) {
	tokenService, err := NewTokenService(cfg)
	if err != nil {
		return nil, err
	}
	verifier, err := identity.NewVerifier(tokenService.ValidateToken)
	if err != nil {
		return nil, err
	}
	return &AuthService{
		users:             users,
		revocations:       revocations,
		tokenService:      tokenService,
		verifier:          verifier,
		passwordValidator: NewPasswordValidator(cfg.PasswordPolicy),
		adminUsername:     cfg.AdminUsername,
	}, nil
}

func (s *AuthService) VerifyToken(tokenString string) (*identity.VerifiedIdentity, error) {
	return s.verifier.VerifyToken(tokenString)
}

func (s *AuthService) SignIn(ctx context.Context, req LoginRequest) (*TokenPair, error) {
	user, err := s.users.GetUserByUsername(ctx, req.Username)
	if err != nil {
		return nil, err
	}

	// Check lockout
	if user.LockoutUntil.After(time.Now()) {
		return nil, ErrAccountLocked
	}

	// Verify password
	valid, err := VerifyPassword(req.Password, user.PasswordHash, user.PasswordAlgo)
	if err != nil {
		return nil, err
	}

	if !valid {
		// Handle failed attempt
		attempts := user.LoginAttempts + 1
		lockoutUntil := user.LockoutUntil
		if attempts >= 10 { // Lockout threshold
			lockoutUntil = time.Now().Add(5 * time.Minute)
		}
		// Log but don't fail the request - login stats are for security monitoring
		if err := s.users.UpdateUserLoginStats(ctx, user.ID, user.LastLoginAt, attempts, lockoutUntil); err != nil {
			slog.Warn("Failed to update login stats for failed attempt",
				"user_id", user.ID,
				"error", err,
			)
		}
		return nil, ErrInvalidCredentials
	}

	// Check disabled
	if user.Disabled {
		return nil, ErrAccountDisabled
	}

	// Success - reset stats
	// Log but don't fail the request - login stats are for security monitoring
	if err := s.users.UpdateUserLoginStats(ctx, user.ID, time.Now(), 0, time.Time{}); err != nil {
		slog.Warn("Failed to reset login stats after successful login",
			"user_id", user.ID,
			"error", err,
		)
	}

	return s.tokenService.GenerateTokenPair(user)
}

func (s *AuthService) SignUp(ctx context.Context, req SignupRequest) (*TokenPair, error) {
	// Check if user already exists
	_, err := s.users.GetUserByUsername(ctx, req.Username)
	if err == nil {
		return nil, errors.New("user already exists")
	}
	if !errors.Is(err, ErrUserNotFound) {
		return nil, err
	}

	// Validate password strength using configured policy
	if err := s.passwordValidator.Validate(req.Password); err != nil {
		return nil, err
	}

	hash, algo, err := HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	user := &User{
		ID:           uuid.New().String(),
		Username:     req.Username,
		PasswordHash: hash,
		PasswordAlgo: algo,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		Disabled:     false,
		Roles:        []string{}, // Default roles
	}

	// Assign admin role to configured admin user
	if s.adminUsername != "" && user.Username == s.adminUsername {
		user.Roles = append(user.Roles, "admin")
	} else {
		user.Roles = append(user.Roles, "user")
	}

	if err := s.users.CreateUser(ctx, user); err != nil {
		return nil, err
	}

	return s.tokenService.GenerateTokenPair(user)
}

func (s *AuthService) Refresh(ctx context.Context, req RefreshRequest) (*TokenPair, error) {
	claims, err := s.tokenService.ValidateToken(req.RefreshToken)
	if err != nil {
		return nil, ErrInvalidToken
	}

	// Atomically check and revoke token to prevent race conditions
	// This ensures only one concurrent refresh request can succeed
	gracePeriod := s.tokenService.RefreshOverlap()
	if err := s.revocations.RevokeTokenIfNotRevoked(ctx, claims.ID, claims.ExpiresAt.Time, gracePeriod); err != nil {
		if errors.Is(err, storage.ErrTokenAlreadyRevoked) {
			return nil, ErrInvalidToken
		}
		return nil, err
	}

	// Get user to ensure still exists/active
	user, err := s.users.GetUserByID(ctx, claims.Subject)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if user.Disabled {
		return nil, ErrAccountDisabled
	}

	// Issue new pair
	return s.tokenService.GenerateTokenPair(user)
}

func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	claims, err := s.tokenService.ValidateToken(refreshToken)
	if err != nil {
		return ErrInvalidToken
	}

	return s.revocations.RevokeTokenImmediate(ctx, claims.ID, claims.ExpiresAt.Time)
}

func (s *AuthService) GenerateSystemToken(serviceName string) (string, error) {
	return s.tokenService.GenerateSystemToken(serviceName)
}

func (s *AuthService) ListUsers(ctx context.Context, actor *identity.VerifiedIdentity, limit int, offset int) ([]*identity.User, error) {
	if err := s.verifier.AuthorizeAdmin(actor); err != nil {
		return nil, err
	}
	users, err := s.users.ListUsers(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	if users == nil {
		return nil, nil
	}
	views := make([]*identity.User, len(users))
	for i, user := range users {
		views[i] = userView(user)
	}
	return views, nil
}

func (s *AuthService) UpdateUser(ctx context.Context, actor *identity.VerifiedIdentity, id string, roles []string, dbAdmin []string, disabled bool) error {
	if err := s.verifier.AuthorizeAdmin(actor); err != nil {
		return err
	}
	user, err := s.users.GetUserByID(ctx, id)
	if err != nil {
		return err
	}

	user.Roles = roles
	user.DBAdmin = dbAdmin
	user.Disabled = disabled
	return s.users.UpdateUser(ctx, user)
}
