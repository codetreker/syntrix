package authn

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/identity/config"
	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/google/uuid"
)

type accountService struct {
	users             repository.UserStore
	revocations       repository.TokenRevocationStore
	signer            *userTokenSigner
	refreshOverlap    time.Duration
	verifier          *identity.Verifier
	passwordValidator *PasswordValidator
	adminUsername     string // Configurable admin username
}

// NewServices loads the signing key once and returns separate account, public
// verification, and system-signing capabilities. Account commands accept actors
// from the returned verifier; initialization failure returns no capabilities.
func NewServices(cfg config.AuthNConfig, users repository.UserStore, revocations repository.TokenRevocationStore) (identity.AccountService, *identity.Verifier, identity.SystemTokenIssuer, error) {
	key, err := EnsurePrivateKey(cfg.PrivateKeyFile)
	if err != nil {
		return nil, nil, nil, err
	}
	verifier, err := identity.NewPublicTokenVerifier(&key.PublicKey)
	if err != nil {
		return nil, nil, nil, err
	}
	accounts := &accountService{
		users: users, revocations: revocations,
		signer:   &userTokenSigner{privateKey: key, accessTTL: cfg.AccessTokenTTL, refreshTTL: cfg.RefreshTokenTTL},
		verifier: verifier, refreshOverlap: cfg.AuthCodeTTL,
		passwordValidator: NewPasswordValidator(cfg.PasswordPolicy), adminUsername: cfg.AdminUsername,
	}
	return accounts, verifier, &systemTokenIssuer{privateKey: key, accessTTL: cfg.AccessTokenTTL}, nil
}

func (s *accountService) SignIn(ctx context.Context, req LoginRequest) (*TokenPair, error) {
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

	return s.signer.generateTokenPair(user)
}

func (s *accountService) SignUp(ctx context.Context, req SignupRequest) (*TokenPair, error) {
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

	user := &repository.UserRecord{
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

	return s.signer.generateTokenPair(user)
}

func (s *accountService) Refresh(ctx context.Context, req RefreshRequest) (*TokenPair, error) {
	actor, err := s.verifier.VerifyToken(req.RefreshToken)
	if err != nil {
		return nil, ErrInvalidToken
	}

	claims := actor.Claims()
	// Atomically check and revoke token to prevent race conditions
	// This ensures only one concurrent refresh request can succeed
	gracePeriod := s.refreshOverlap
	if err := s.revocations.RevokeTokenIfNotRevoked(ctx, claims.ID, claims.ExpiresAt.Time, gracePeriod); err != nil {
		if errors.Is(err, repository.ErrTokenAlreadyRevoked) {
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
	return s.signer.generateTokenPair(user)
}

func (s *accountService) Logout(ctx context.Context, refreshToken string) error {
	actor, err := s.verifier.VerifyToken(refreshToken)
	if err != nil {
		return ErrInvalidToken
	}

	claims := actor.Claims()
	return s.revocations.RevokeTokenImmediate(ctx, claims.ID, claims.ExpiresAt.Time)
}

func (s *accountService) ListUsers(ctx context.Context, actor *identity.VerifiedIdentity, limit int, offset int) ([]*identity.User, error) {
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

func (s *accountService) UpdateUser(ctx context.Context, actor *identity.VerifiedIdentity, id string, roles []string, dbAdmin []string, disabled bool) error {
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
