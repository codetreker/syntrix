package authn

import (
	identtypes "github.com/codetreker/syntrix/internal/core/identity/types"
	"github.com/codetreker/syntrix/internal/core/storage"
)

// Directly reuse public identity types to avoid duplicate definitions and adapters.
type (
	ContextKey     = identtypes.ContextKey
	Claims         = identtypes.Claims
	TokenPair      = identtypes.TokenPair
	LoginRequest   = identtypes.LoginRequest
	SignupRequest  = identtypes.SignupRequest
	RefreshRequest = identtypes.RefreshRequest
)

const (
	ContextKeyUser     = identtypes.ContextKeyUser
	ContextKeyUserID   = identtypes.ContextKeyUserID
	ContextKeyUsername = identtypes.ContextKeyUsername
	ContextKeyRoles    = identtypes.ContextKeyRoles
	ContextKeyClaims   = identtypes.ContextKeyClaims
	ContextKeyDBAdmin  = identtypes.ContextKeyDBAdmin
)

// Keep storage alias for store interfaces.
type User = storage.User
