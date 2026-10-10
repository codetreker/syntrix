package authn

import (
	"crypto/rsa"
	"time"

	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type userTokenSigner struct {
	privateKey *rsa.PrivateKey
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func (s *userTokenSigner) generateTokenPair(user *repository.UserRecord) (*TokenPair, error) {
	now := time.Now()
	jti := uuid.New().String()

	// Collect db_admin from user (if user is admin for certain databases)
	var dbAdmin []string
	if user.DBAdmin != nil {
		dbAdmin = user.DBAdmin
	}

	// Access Token
	accessClaims := Claims{
		Username: user.Username,
		Roles:    user.Roles,
		Disabled: user.Disabled,
		DBAdmin:  dbAdmin,
		UserID:   user.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        jti, // Use same JTI or different? Usually different.
		},
	}
	// Override JTI for access token to be unique
	accessClaims.ID = uuid.New().String()

	accessToken := jwt.NewWithClaims(jwt.SigningMethodRS256, accessClaims)
	accessTokenString, err := accessToken.SignedString(s.privateKey)
	if err != nil {
		return nil, err
	}

	// Refresh Token
	refreshClaims := Claims{
		Username: user.Username,
		DBAdmin:  dbAdmin,
		UserID:   user.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        jti, // This JTI is tracked for rotation
		},
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodRS256, refreshClaims)
	refreshTokenString, err := refreshToken.SignedString(s.privateKey)
	if err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  accessTokenString,
		RefreshToken: refreshTokenString,
		ExpiresIn:    int(s.accessTTL.Seconds()),
	}, nil
}
