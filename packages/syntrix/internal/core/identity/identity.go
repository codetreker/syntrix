package identity

import (
	"github.com/codetreker/syntrix/internal/core/identity/authn"
	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/internal/core/storage"
)

// Errors from authn
var (
	ErrInvalidCredentials = authn.ErrInvalidCredentials
	ErrAccountDisabled    = authn.ErrAccountDisabled
	ErrAccountLocked      = authn.ErrAccountLocked
	ErrInvalidToken       = authn.ErrInvalidToken
	ErrUserNotFound       = authn.ErrUserNotFound
	ErrUserExists         = authn.ErrUserExists
)

type AuthN = authn.Service

// NewAuthN creates a new authentication service.
func NewAuthN(cfg config.AuthNConfig, users storage.UserStore, revocations storage.TokenRevocationStore) (AuthN, error) {
	return authn.NewAuthService(cfg, users, revocations)
}
