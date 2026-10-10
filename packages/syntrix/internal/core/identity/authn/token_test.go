package authn

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenCapabilities_GenerateAndValidate(t *testing.T) {
	keyFile := getTestKeyPath(t)
	cfg := config.AuthNConfig{
		PrivateKeyFile:  keyFile,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 1 * time.Hour,
		AuthCodeTTL:     2 * time.Minute,
	}

	signer, verifier := tokenCapabilitiesForTest(t, cfg)

	user := &User{
		ID:       "user-123",
		Username: "testuser",
		Roles:    []string{"admin"},
		DBAdmin:  []string{"db1"},
		Disabled: true,
	}

	// Generate
	pair, err := signer.generateTokenPair(user)
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)
	assert.Equal(t, 900, pair.ExpiresIn) // 15 minutes in seconds

	// Validate Access Token
	actor, err := verifier.VerifyToken(pair.AccessToken)
	require.NoError(t, err)
	claims := actor.Claims()
	assert.Equal(t, user.ID, claims.Subject)
	assert.Equal(t, user.Username, claims.Username)
	assert.Equal(t, user.Roles, claims.Roles)
	assert.Equal(t, user.ID, claims.UserID)
	assert.Equal(t, user.DBAdmin, claims.DBAdmin)
	assert.Equal(t, user.Disabled, claims.Disabled)
	assert.Equal(t, cfg.AccessTokenTTL, claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time))
	assert.Equal(t, claims.IssuedAt, claims.NotBefore)
	assert.Empty(t, claims.Issuer)
	assert.Nil(t, claims.Audience)

	// Validate Refresh Token
	refreshActor, err := verifier.VerifyToken(pair.RefreshToken)
	require.NoError(t, err)
	refreshClaims := refreshActor.Claims()
	assert.Equal(t, user.ID, refreshClaims.Subject)
	assert.Equal(t, user.Username, refreshClaims.Username)
	assert.Equal(t, user.ID, refreshClaims.UserID)
	assert.Equal(t, user.DBAdmin, refreshClaims.DBAdmin)
	assert.Nil(t, refreshClaims.Roles)
	assert.False(t, refreshClaims.Disabled)
	assert.Equal(t, cfg.RefreshTokenTTL, refreshClaims.ExpiresAt.Time.Sub(refreshClaims.IssuedAt.Time))
	assert.Equal(t, refreshClaims.IssuedAt, refreshClaims.NotBefore)
	assert.NotEmpty(t, claims.ID)
	assert.NotEmpty(t, refreshClaims.ID)
	assert.NotEqual(t, claims.ID, refreshClaims.ID)
}

func TestTokenCapabilities_ExpiredToken(t *testing.T) {
	// Create service with very short TTL
	keyFile := getTestKeyPath(t)
	cfg := config.AuthNConfig{
		PrivateKeyFile:  keyFile,
		AccessTokenTTL:  1 * time.Millisecond,
		RefreshTokenTTL: 1 * time.Millisecond,
	}
	signer, verifier := tokenCapabilitiesForTest(t, cfg)

	user := &User{ID: "user-1", Username: "user"}
	pair, err := signer.generateTokenPair(user)
	require.NoError(t, err)

	// Wait for expiration
	time.Sleep(2 * time.Millisecond)

	// Validate
	_, err = verifier.VerifyToken(pair.AccessToken)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token is expired")
}

func TestTokenCapabilities_InvalidSignature(t *testing.T) {
	keyFile1 := getTestKeyPath(t)
	cfg1 := config.AuthNConfig{
		PrivateKeyFile:  keyFile1,
		AccessTokenTTL:  1 * time.Hour,
		RefreshTokenTTL: 1 * time.Hour,
	}
	signer1, _ := tokenCapabilitiesForTest(t, cfg1)

	keyFile2 := filepath.Join(t.TempDir(), "key2.pem")
	cfg2 := config.AuthNConfig{
		PrivateKeyFile:  keyFile2,
		AccessTokenTTL:  1 * time.Hour,
		RefreshTokenTTL: 1 * time.Hour,
	}
	_, verifier2 := tokenCapabilitiesForTest(t, cfg2)

	user := &User{ID: "user-1", Username: "user"}
	pair, _ := signer1.generateTokenPair(user)

	// Validation uses a different public key.
	_, err := verifier2.VerifyToken(pair.AccessToken)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "verification error")
}

func TestTokenCapabilities_SaveAndLoadPrivateKey(t *testing.T) {
	key, err := GeneratePrivateKey()
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "key.pem")
	require.NoError(t, SavePrivateKey(path, key))

	loaded, err := LoadPrivateKey(path)
	require.NoError(t, err)
	assert.Equal(t, key.PublicKey.N, loaded.PublicKey.N)
}

func TestTokenCapabilities_GenerateSystemToken(t *testing.T) {
	keyFile := getTestKeyPath(t)
	cfg := config.AuthNConfig{
		PrivateKeyFile:  keyFile,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 1 * time.Hour,
		AuthCodeTTL:     2 * time.Minute,
	}
	_, verifier := tokenCapabilitiesForTest(t, cfg)
	issuer, err := NewSystemTokenIssuer(cfg)
	require.NoError(t, err)

	token, err := issuer.GenerateSystemToken("worker")
	require.NoError(t, err)

	actor, err := verifier.VerifyToken(token)
	require.NoError(t, err)
	claims := actor.Claims()
	assert.Equal(t, "system:worker", claims.Subject)
	assert.Contains(t, claims.Roles, "system")
	assert.Contains(t, claims.Roles, "service:worker")
}

func TestLoadPrivateKey(t *testing.T) {
	// Test case 1: File does not exist
	_, err := LoadPrivateKey("non_existent_key.pem")
	assert.Error(t, err)

	// Test case 2: Invalid PEM content
	tmpDir := t.TempDir()
	invalidKeyPath := filepath.Join(tmpDir, "invalid.pem")
	err = os.WriteFile(invalidKeyPath, []byte("invalid pem content"), 0600)
	require.NoError(t, err)

	_, err = LoadPrivateKey(invalidKeyPath)
	assert.Error(t, err)

	// Test case 3: Valid Key
	validKeyPath := filepath.Join(tmpDir, "valid.pem")
	key, err := GeneratePrivateKey()
	require.NoError(t, err)

	// Save the generated key to file
	keyBytes := x509.MarshalPKCS1PrivateKey(key)
	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: keyBytes,
	}

	f, err := os.Create(validKeyPath)
	require.NoError(t, err)
	err = pem.Encode(f, pemBlock)
	require.NoError(t, err)
	f.Close()

	loadedKey, err := LoadPrivateKey(validKeyPath)
	require.NoError(t, err)
	assert.NotNil(t, loadedKey)
	assert.Equal(t, key.N, loadedKey.N)
	assert.Equal(t, key.E, loadedKey.E)
}

func TestEnsurePrivateKey(t *testing.T) {
	tempDir := t.TempDir()

	// Case 1: File does not exist, should generate
	keyPath := filepath.Join(tempDir, "new_key.pem")
	key, err := EnsurePrivateKey(keyPath)
	require.NoError(t, err)
	assert.NotNil(t, key)
	assert.FileExists(t, keyPath)

	// Case 2: File exists, should load
	key2, err := EnsurePrivateKey(keyPath)
	require.NoError(t, err)
	assert.Equal(t, key.N, key2.N)

	// Case 3: Invalid path (directory creation failure or save failure)
	// Using a path where directory cannot be created (e.g. file as directory)
	dummyFile := filepath.Join(tempDir, "dummy")
	os.WriteFile(dummyFile, []byte("dummy"), 0644)
	invalidPath := filepath.Join(dummyFile, "key.pem")

	_, err = EnsurePrivateKey(invalidPath)
	assert.Error(t, err)
}

func tokenCapabilitiesForTest(t *testing.T, cfg config.AuthNConfig) (*userTokenSigner, *identity.Verifier) {
	t.Helper()
	accounts, verifier, _, err := NewServices(cfg, nil, nil)
	require.NoError(t, err)
	return accounts.(*accountService).signer, verifier
}
