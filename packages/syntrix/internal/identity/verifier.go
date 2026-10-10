package identity

import (
	"errors"
	"slices"

	"github.com/golang-jwt/jwt/v5"
)

type authority struct{ initialized bool }

// VerifiedIdentity keeps the validation authority and claims private so callers
// cannot mint actors or alter the privileges used by account commands.
type VerifiedIdentity struct {
	authority *authority
	claims    Claims
}

type Verifier struct {
	validate  func(string) (*Claims, error)
	authority *authority
}

func NewVerifier(validate func(string) (*Claims, error)) (*Verifier, error) {
	if validate == nil {
		return nil, errors.New("token validator is required")
	}
	return &Verifier{validate: validate, authority: &authority{initialized: true}}, nil
}

func (v *Verifier) VerifyToken(token string) (*VerifiedIdentity, error) {
	if v == nil || v.validate == nil || v.authority == nil || !v.authority.initialized {
		return nil, ErrInvalidToken
	}
	claims, err := v.validate(token)
	if err != nil {
		return nil, err
	}
	if claims == nil {
		return nil, ErrInvalidToken
	}
	return &VerifiedIdentity{authority: v.authority, claims: cloneClaims(*claims)}, nil
}

func (v *Verifier) AuthorizeAdmin(actor *VerifiedIdentity) error {
	if v == nil || v.validate == nil || v.authority == nil || !v.authority.initialized || actor == nil || actor.authority != v.authority {
		return ErrAdminRequired
	}
	for _, role := range actor.claims.Roles {
		if role == "admin" || role == "system" {
			return nil
		}
	}
	return ErrAdminRequired
}

func (a *VerifiedIdentity) Claims() *Claims {
	claims := cloneClaims(a.claims)
	return &claims
}

func cloneClaims(claims Claims) Claims {
	claims.Roles = slices.Clone(claims.Roles)
	claims.DBAdmin = slices.Clone(claims.DBAdmin)
	claims.Audience = slices.Clone(claims.Audience)
	cloneDate := func(date *jwt.NumericDate) *jwt.NumericDate {
		if date == nil {
			return nil
		}
		copy := *date
		return &copy
	}
	claims.ExpiresAt = cloneDate(claims.ExpiresAt)
	claims.NotBefore = cloneDate(claims.NotBefore)
	claims.IssuedAt = cloneDate(claims.IssuedAt)
	return claims
}
