package authn

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type TokenService struct {
	privateKey     *rsa.PrivateKey
	publicKey      *rsa.PublicKey
	accessTTL      time.Duration
	refreshTTL     time.Duration
	refreshOverlap time.Duration
}

func (s *TokenService) RefreshOverlap() time.Duration {
	return s.refreshOverlap
}

func NewTokenService(cfg config.AuthNConfig) (*TokenService, error) {
	key, err := EnsurePrivateKey(cfg.PrivateKeyFile)
	if err != nil {
		return nil, err
	}

	return &TokenService{
		privateKey:     key,
		publicKey:      &key.PublicKey,
		accessTTL:      cfg.AccessTokenTTL,
		refreshTTL:     cfg.RefreshTokenTTL,
		refreshOverlap: cfg.AuthCodeTTL,
	}, nil
}

func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	// Check file permissions on Unix systems
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err == nil {
			mode := info.Mode().Perm()
			// Check if group or others have any permissions
			if mode&0077 != 0 {
				slog.Warn("Private key file has insecure permissions",
					"path", path,
					"mode", fmt.Sprintf("%04o", mode),
					"recommended", "0600",
				)
			}
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("failed to decode PEM block containing private key")
	}

	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func EnsurePrivateKey(path string) (*rsa.PrivateKey, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		log.Printf("[Warning][AuthN] Private key not found at %s, generating new key...", path)
		// Generate new key
		key, err := GeneratePrivateKey()
		if err != nil {
			return nil, fmt.Errorf("failed to generate key: %w", err)
		}

		// Ensure directory exists
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}

		// Save key
		if err := SavePrivateKey(path, key); err != nil {
			return nil, fmt.Errorf("failed to save key: %w", err)
		}
		return key, nil
	}

	// Load existing key
	return LoadPrivateKey(path)
}

func SavePrivateKey(path string, key *rsa.PrivateKey) error {
	// Create file with secure permissions (0600 = owner read/write only)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer file.Close()

	privateKeyBytes := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	}

	return pem.Encode(file, block)
}

func GeneratePrivateKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
}

func (s *TokenService) GenerateTokenPair(user *User) (*TokenPair, error) {
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

func (s *TokenService) GenerateSystemToken(serviceName string) (string, error) {
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

func (s *TokenService) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Explicitly validate RS256 algorithm to prevent algorithm confusion attacks
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.publicKey, nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}
