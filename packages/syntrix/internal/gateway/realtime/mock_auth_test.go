package realtime

import (
	"context"

	"github.com/codetreker/syntrix/internal/ctxkeys"
	"github.com/codetreker/syntrix/internal/identity"
)

// mockAuthService validates the realtime test token.
type mockAuthService struct{}

func (m *mockAuthService) validateClaims(tokenString string) (*identity.Claims, error) {
	if tokenString != "good" {
		return nil, identity.ErrInvalidToken
	}
	// Database is now extracted from auth payload, not from token
	return &identity.Claims{Roles: []string{"user"}}, nil
}

func withRoles(ctx context.Context, roles []string) context.Context {
	ctx = context.WithValue(ctx, ctxkeys.KeyRoles, roles)
	return ctx
}

func (m *mockAuthService) VerifyToken(token string) (*identity.VerifiedIdentity, error) {
	verifier, _ := identity.NewVerifier(m.validateClaims)
	return verifier.VerifyToken(token)
}
