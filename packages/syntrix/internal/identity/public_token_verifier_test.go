package identity

import (
	"crypto/rand"
	"crypto/rsa"
	"math/big"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicTokenVerifierCopiesPublicMaterial(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	claims := Claims{Username: "user", UserID: "user-id", Roles: []string{"admin"}, DBAdmin: []string{"db"},
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-id", ID: "jti", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	require.NoError(t, err)
	public := &rsa.PublicKey{N: new(big.Int).Set(key.N), E: key.E}
	verifier, err := NewPublicTokenVerifier(public)
	require.NoError(t, err)
	public.N.SetInt64(1)
	public.E = 1
	actor, err := verifier.VerifyToken(token)
	require.NoError(t, err)
	assert.Equal(t, claims, *actor.Claims())
	assert.NoError(t, verifier.AuthorizeAdmin(actor))
	assert.NotImplements(t, (*SystemTokenIssuer)(nil), verifier)
	assert.NotImplements(t, (*AccountService)(nil), verifier)
	for _, invalid := range []*rsa.PublicKey{nil, {E: 65537}} {
		_, err := NewPublicTokenVerifier(invalid)
		assert.EqualError(t, err, "RSA public key is required")
	}
}

func TestPublicTokenVerifierPreservesJWTValidation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	verifier, err := NewPublicTokenVerifier(&key.PublicKey)
	require.NoError(t, err)
	now := time.Now()
	for _, tc := range []struct {
		name      string
		method    jwt.SigningMethod
		key       interface{}
		claims    Claims
		errorText string
	}{
		{name: "valid without expiry", method: jwt.SigningMethodRS256, key: key, claims: Claims{Disabled: true}},
		{name: "expired", method: jwt.SigningMethodRS256, key: key, claims: Claims{RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(now.Add(-time.Hour))}}, errorText: "token is expired"},
		{name: "not yet valid", method: jwt.SigningMethodRS256, key: key, claims: Claims{RegisteredClaims: jwt.RegisteredClaims{NotBefore: jwt.NewNumericDate(now.Add(time.Hour))}}, errorText: "token is not valid yet"},
		{name: "wrong key", method: jwt.SigningMethodRS256, key: other, errorText: "verification error"},
		{name: "RSA algorithm mismatch", method: jwt.SigningMethodRS512, key: key, errorText: "unexpected signing method"},
		{name: "HMAC", method: jwt.SigningMethodHS256, key: []byte("secret"), errorText: "unexpected signing method"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := jwt.NewWithClaims(tc.method, tc.claims).SignedString(tc.key)
			require.NoError(t, err)
			actor, err := verifier.VerifyToken(token)
			if tc.errorText != "" {
				assert.ErrorContains(t, err, tc.errorText)
				assert.Nil(t, actor)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.claims, *actor.Claims())
			}
		})
	}
	_, err = verifier.VerifyToken("not.a.token")
	assert.Error(t, err)
}
