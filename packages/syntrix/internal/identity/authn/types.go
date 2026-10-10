package authn

import (
	"github.com/codetreker/syntrix/internal/identity"
)

type (
	Claims         = identity.Claims
	TokenPair      = identity.TokenPair
	LoginRequest   = identity.LoginRequest
	SignupRequest  = identity.SignupRequest
	RefreshRequest = identity.RefreshRequest
)
