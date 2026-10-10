package authn

import "github.com/codetreker/syntrix/internal/identity"

var (
	ErrInvalidCredentials = identity.ErrInvalidCredentials
	ErrAccountDisabled    = identity.ErrAccountDisabled
	ErrAccountLocked      = identity.ErrAccountLocked
	ErrInvalidToken       = identity.ErrInvalidToken
	ErrUserNotFound       = identity.ErrUserNotFound
	ErrUserExists         = identity.ErrUserExists
)
