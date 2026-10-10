package identity

import "github.com/codetreker/syntrix/internal/core/identity/types"

type (
	ContextKey     = types.ContextKey
	Claims         = types.Claims
	TokenPair      = types.TokenPair
	LoginRequest   = types.LoginRequest
	SignupRequest  = types.SignupRequest
	RefreshRequest = types.RefreshRequest
	User           = types.User
)

const (
	ContextKeyUser     = types.ContextKeyUser
	ContextKeyUserID   = types.ContextKeyUserID
	ContextKeyUsername = types.ContextKeyUsername
	ContextKeyRoles    = types.ContextKeyRoles
	ContextKeyClaims   = types.ContextKeyClaims
	ContextKeyDBAdmin  = types.ContextKeyDBAdmin
)
