package authn

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenService_GenerateAndValidate(t *testing.T) {
	keyFile := getTestKeyPath(t)
	cfg := config.AuthNConfig{
		PrivateKeyFile:  keyFile,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 1 * time.Hour,
		AuthCodeTTL:     2 * time.Minute,
	}

	ts, err := NewTokenService(cfg)
	require.NoError(t, err)

	user := &User{
		ID:       "user-123",
		Username: "testuser",
		Roles:    []string{"admin"},
		Disabled: false,
	}

	// Generate
	pair, err := ts.GenerateTokenPair(user)
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)
	assert.Equal(t, 900, pair.ExpiresIn) // 15 minutes in seconds

	// Validate Access Token
	claims, err := ts.ValidateToken(pair.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, user.ID, claims.Subject)
	assert.Equal(t, user.Username, claims.Username)
	assert.Equal(t, user.Roles, claims.Roles)

	// Validate Refresh Token
	refreshClaims, err := ts.ValidateToken(pair.RefreshToken)
	require.NoError(t, err)
	assert.Equal(t, user.ID, refreshClaims.Subject)
	assert.Equal(t, user.Username, refreshClaims.Username)
}

func TestTokenService_ExpiredToken(t *testing.T) {
	// Create service with very short TTL
	keyFile := getTestKeyPath(t)
	cfg := config.AuthNConfig{
		PrivateKeyFile:  keyFile,
		AccessTokenTTL:  1 * time.Millisecond,
		RefreshTokenTTL: 1 * time.Millisecond,
	}
	ts, err := NewTokenService(cfg)
	require.NoError(t, err)

	user := &User{ID: "user-1", Username: "user"}
	pair, err := ts.GenerateTokenPair(user)
	require.NoError(t, err)

	// Wait for expiration
	time.Sleep(2 * time.Millisecond)

	// Validate
	_, err = ts.ValidateToken(pair.AccessToken)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token is expired")
}

func TestTokenService_InvalidSignature(t *testing.T) {
	keyFile1 := getTestKeyPath(t)
	cfg1 := config.AuthNConfig{
		PrivateKeyFile:  keyFile1,
		AccessTokenTTL:  1 * time.Hour,
		RefreshTokenTTL: 1 * time.Hour,
	}
	ts1, _ := NewTokenService(cfg1)

	keyFile2 := filepath.Join(t.TempDir(), "key2.pem")
	cfg2 := config.AuthNConfig{
		PrivateKeyFile:  keyFile2,
		AccessTokenTTL:  1 * time.Hour,
		RefreshTokenTTL: 1 * time.Hour,
	}
	ts2, _ := NewTokenService(cfg2) // Different keys

	user := &User{ID: "user-1", Username: "user"}
	pair, _ := ts1.GenerateTokenPair(user)

	// Try to validate with ts2 (different public key)
	_, err := ts2.ValidateToken(pair.AccessToken)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "verification error")
}

func TestTokenService_SaveAndLoadPrivateKey(t *testing.T) {
	key, err := GeneratePrivateKey()
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "key.pem")
	require.NoError(t, SavePrivateKey(path, key))

	loaded, err := LoadPrivateKey(path)
	require.NoError(t, err)
	assert.Equal(t, key.PublicKey.N, loaded.PublicKey.N)
}

func TestTokenService_GenerateSystemToken(t *testing.T) {
	keyFile := getTestKeyPath(t)
	cfg := config.AuthNConfig{
		PrivateKeyFile:  keyFile,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 1 * time.Hour,
		AuthCodeTTL:     2 * time.Minute,
	}
	ts, err := NewTokenService(cfg)
	require.NoError(t, err)

	token, err := ts.GenerateSystemToken("worker")
	require.NoError(t, err)

	claims, err := ts.ValidateToken(token)
	require.NoError(t, err)
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
