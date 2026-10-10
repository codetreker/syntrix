package authn

import (
	"crypto/rsa"
	"time"

	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/identity/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type systemTokenIssuer struct {
	privateKey *rsa.PrivateKey
	accessTTL  time.Duration
}

// NewSystemTokenIssuer loads or creates the configured key for callers that only
// issue system tokens. Instance composition uses NewServices to share one load.
func NewSystemTokenIssuer(cfg config.AuthNConfig) (identity.SystemTokenIssuer, error) {
	key, err := EnsurePrivateKey(cfg.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	return &systemTokenIssuer{privateKey: key, accessTTL: cfg.AccessTokenTTL}, nil
}

func (s *systemTokenIssuer) GenerateSystemToken(serviceName string) (string, error) {
	now := time.Now()
	jti := uuid.New().String()

	claims := Claims{
		Username: "system:" + serviceName,
		UserID:   "system:" + serviceName,
		Roles:    []string{"system", "service:" + serviceName},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "system:" + serviceName,
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        jti,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(s.privateKey)
}
