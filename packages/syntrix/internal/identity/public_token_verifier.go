package identity

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"math/big"

	"github.com/golang-jwt/jwt/v5"
)

// NewPublicTokenVerifier copies the RSA public material and retains no signer or
// storage. Account commands authorize its actors only when they use this same
// verifier instance.
func NewPublicTokenVerifier(publicKey *rsa.PublicKey) (*Verifier, error) {
	if publicKey == nil || publicKey.N == nil {
		return nil, errors.New("RSA public key is required")
	}
	key := &rsa.PublicKey{N: new(big.Int).Set(publicKey.N), E: publicKey.E}
	return NewVerifier(func(token string) (*Claims, error) { return validatePublicToken(key, token) })
}

func validatePublicToken(publicKey *rsa.PublicKey, tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Explicitly validate RS256 algorithm to prevent algorithm confusion attacks
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return publicKey, nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}
