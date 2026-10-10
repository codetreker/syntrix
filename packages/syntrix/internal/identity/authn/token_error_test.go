package authn

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/identity/config"
	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenCapabilities_ErrorPaths(t *testing.T) {
	t.Parallel()
	t.Run("NewSystemTokenIssuer_InvalidKeyPath", func(t *testing.T) {
		// Use a path that cannot be written to (e.g., under a file treated as dir)
		tmpDir := t.TempDir()
		dummyFile := filepath.Join(tmpDir, "file")
		os.WriteFile(dummyFile, []byte("content"), 0600)

		cfg := config.AuthNConfig{
			PrivateKeyFile: filepath.Join(dummyFile, "key.pem"),
		}
		_, err := NewSystemTokenIssuer(cfg)
		assert.Error(t, err)
	})

	t.Run("LoadPrivateKey_InvalidPEM", func(t *testing.T) {
		tmpDir := t.TempDir()
		keyPath := filepath.Join(tmpDir, "invalid.pem")
		os.WriteFile(keyPath, []byte("-----BEGIN RSA PRIVATE KEY-----\nINVALID\n-----END RSA PRIVATE KEY-----"), 0600)

		_, err := LoadPrivateKey(keyPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode PEM block")
	})

	t.Run("SavePrivateKey_InvalidPath", func(t *testing.T) {
		key, _ := GeneratePrivateKey()
		// Try to save to a directory path
		tmpDir := t.TempDir()
		err := SavePrivateKey(tmpDir, key)
		assert.Error(t, err)
	})

	t.Run("ValidateToken_InvalidSigningMethod", func(t *testing.T) {
		// Create a token signed with HMAC instead of RSA
		token := jwt.New(jwt.SigningMethodHS256)
		tokenString, _ := token.SignedString([]byte("secret"))

		keyFile := getTestKeyPath(t)
		cfg := config.AuthNConfig{PrivateKeyFile: keyFile}
		_, verifier := tokenCapabilitiesForTest(t, cfg)

		_, err := verifier.VerifyToken(tokenString)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected signing method")
	})

	t.Run("ValidateToken_MalformedToken", func(t *testing.T) {
		keyFile := getTestKeyPath(t)
		cfg := config.AuthNConfig{PrivateKeyFile: keyFile}
		_, verifier := tokenCapabilitiesForTest(t, cfg)

		_, err := verifier.VerifyToken("not.a.token")
		assert.Error(t, err)
	})
}

// Mock reader that returns error
type errorReader struct{}

func (r *errorReader) Read(p []byte) (n int, err error) {
	return 0, assert.AnError
}

func TestGeneratePrivateKey_Error(t *testing.T) {
	// Backup rand.Reader
	origReader := rand.Reader
	defer func() { rand.Reader = origReader }()

	// Inject error reader
	rand.Reader = &errorReader{}

	_, err := GeneratePrivateKey()
	assert.Error(t, err)
}

func TestEnsurePrivateKey_GenerateError(t *testing.T) {
	// Backup rand.Reader
	origReader := rand.Reader
	defer func() { rand.Reader = origReader }()

	// Inject error reader to cause GeneratePrivateKey to fail
	rand.Reader = &errorReader{}

	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.pem")

	_, err := EnsurePrivateKey(keyPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to generate key")
}

func TestTokenCapabilities_GenerateTokenPair_ValidToken(t *testing.T) {
	keyFile := getTestKeyPath(t)
	cfg := config.AuthNConfig{
		PrivateKeyFile: keyFile,
		AccessTokenTTL: 1 * time.Hour, // Ensure token doesn't expire immediately
	}
	signer, verifier := tokenCapabilitiesForTest(t, cfg)

	user := &repository.UserRecord{ID: "u1", Username: "user", Roles: []string{"user"}}
	pair, err := signer.generateTokenPair(user)
	require.NoError(t, err)

	actor, err := verifier.VerifyToken(pair.AccessToken)
	require.NoError(t, err)
	// Database is no longer in claims, but we can verify roles and user ID
	claims := actor.Claims()
	assert.Equal(t, "u1", claims.Subject)
	assert.Contains(t, claims.Roles, "user")
}
