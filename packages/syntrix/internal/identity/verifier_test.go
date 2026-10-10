package identity

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifierAuthorityAndClaims(t *testing.T) {
	now := time.Now()
	source := &Claims{Username: "operator", Roles: []string{"admin"}, DBAdmin: []string{"db"}, UserID: "oid", Disabled: true,
		RegisteredClaims: jwt.RegisteredClaims{Subject: "subject", Audience: jwt.ClaimStrings{"aud"}, Issuer: "issuer", ID: "jti",
			ExpiresAt: &jwt.NumericDate{Time: now}, NotBefore: &jwt.NumericDate{Time: now.Add(-time.Minute)}, IssuedAt: &jwt.NumericDate{Time: now.Add(-time.Hour)}},
	}
	verifier, err := NewVerifier(func(string) (*Claims, error) { return source, nil })
	require.NoError(t, err)
	actor, err := verifier.VerifyToken("token")
	require.NoError(t, err)
	expected := actor.Claims()
	assert.Equal(t, source, expected)
	source.Roles[0], source.DBAdmin[0], source.Audience[0] = "user", "changed", "changed"
	source.ExpiresAt.Time, source.NotBefore.Time, source.IssuedAt.Time = time.Time{}, time.Time{}, time.Time{}
	view := actor.Claims()
	view.Roles[0], view.DBAdmin[0], view.Audience[0] = "user", "changed", "changed"
	view.ExpiresAt.Time, view.NotBefore.Time, view.IssuedAt.Time = time.Time{}, time.Time{}, time.Time{}
	assert.Equal(t, expected, actor.Claims())
	require.NoError(t, verifier.AuthorizeAdmin(actor))
	foreign, err := NewVerifier(func(string) (*Claims, error) { return expected, nil })
	require.NoError(t, err)
	foreignActor, err := foreign.VerifyToken("token")
	require.NoError(t, err)
	assert.ErrorIs(t, verifier.AuthorizeAdmin(foreignActor), ErrAdminRequired)
	for _, invalid := range []*VerifiedIdentity{nil, {}} {
		assert.ErrorIs(t, verifier.AuthorizeAdmin(invalid), ErrAdminRequired)
	}
	for _, uninitialized := range []*Verifier{nil, {}} {
		_, err := uninitialized.VerifyToken("token")
		assert.ErrorIs(t, err, ErrInvalidToken)
		assert.ErrorIs(t, uninitialized.AuthorizeAdmin(actor), ErrAdminRequired)
	}
}

func TestVerifierValidationAndRoles(t *testing.T) {
	_, err := NewVerifier(nil)
	require.EqualError(t, err, "token validator is required")
	wantErr := errors.New("validation failed")
	for _, tc := range []struct {
		claims *Claims
		err    error
	}{{err: wantErr}, {}} {
		verifier, err := NewVerifier(func(string) (*Claims, error) { return tc.claims, tc.err })
		require.NoError(t, err)
		actor, err := verifier.VerifyToken("token")
		assert.Nil(t, actor)
		if tc.err != nil {
			assert.ErrorIs(t, err, wantErr)
		} else {
			assert.ErrorIs(t, err, ErrInvalidToken)
		}
	}
	for _, roles := range [][]string{nil, {}, {"user"}, {"Admin"}, {"SYSTEM"}, {"admin"}, {"system"}} {
		verifier, err := NewVerifier(func(string) (*Claims, error) { return &Claims{Roles: roles}, nil })
		require.NoError(t, err)
		actor, err := verifier.VerifyToken("token")
		require.NoError(t, err)
		assert.Equal(t, roles, actor.Claims().Roles)
		assert.Nil(t, actor.Claims().DBAdmin)
		assert.Nil(t, actor.Claims().Audience)
		if len(roles) == 1 && (roles[0] == "admin" || roles[0] == "system") {
			assert.NoError(t, verifier.AuthorizeAdmin(actor))
		} else {
			assert.ErrorIs(t, verifier.AuthorizeAdmin(actor), ErrAdminRequired)
		}
	}
}
