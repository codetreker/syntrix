// Package utils provides utility functions for benchmark operations.
package utils

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity/authn"
	"github.com/codetreker/syntrix/internal/core/identity/config"
)

// TokenSource represents a source for authentication tokens.
type TokenSource interface {
	// GetToken returns the authentication token.
	GetToken() (string, error)
}

// StaticTokenSource is a token source that returns a static token.
type StaticTokenSource struct {
	token string
}

// NewStaticTokenSource creates a new static token source.
func NewStaticTokenSource(token string) *StaticTokenSource {
	return &StaticTokenSource{token: token}
}

// GetToken returns the static token.
func (s *StaticTokenSource) GetToken() (string, error) {
	if s.token == "" {
		return "", fmt.Errorf("token is empty")
	}
	return s.token, nil
}

// EnvTokenSource is a token source that reads from environment variables.
type EnvTokenSource struct {
	envVar string
}

// NewEnvTokenSource creates a new environment variable token source.
func NewEnvTokenSource(envVar string) *EnvTokenSource {
	if envVar == "" {
		envVar = "SYNTRIX_TOKEN"
	}
	return &EnvTokenSource{envVar: envVar}
}

// GetToken reads the token from the environment variable.
func (e *EnvTokenSource) GetToken() (string, error) {
	token := os.Getenv(e.envVar)
	if token == "" {
		return "", fmt.Errorf("environment variable %s is not set or empty", e.envVar)
	}
	return token, nil
}

// FileTokenSource is a token source that reads from a file.
type FileTokenSource struct {
	path string
}

// NewFileTokenSource creates a new file token source.
func NewFileTokenSource(path string) *FileTokenSource {
	return &FileTokenSource{path: path}
}

// GetToken reads the token from the file.
func (f *FileTokenSource) GetToken() (string, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return "", fmt.Errorf("failed to read token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file %s is empty", f.path)
	}
	return token, nil
}

// GenerateTestToken generates a random token for testing purposes.
func GenerateTestToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}

// LoadToken loads a token from the configuration or environment.
// It tries the following sources in order:
// 1. Direct token value (if not empty)
// 2. Environment variable (if token starts with "env:")
// 3. File path (if token starts with "file:")
func LoadToken(tokenConfig string) (string, error) {
	if tokenConfig == "" {
		return "", fmt.Errorf("token configuration is empty")
	}

	// Direct token value
	if !strings.HasPrefix(tokenConfig, "env:") && !strings.HasPrefix(tokenConfig, "file:") {
		return tokenConfig, nil
	}

	// Environment variable
	if strings.HasPrefix(tokenConfig, "env:") {
		envVar := strings.TrimPrefix(tokenConfig, "env:")
		if envVar == "" {
			envVar = "SYNTRIX_TOKEN"
		}
		source := NewEnvTokenSource(envVar)
		return source.GetToken()
	}

	// File path
	if strings.HasPrefix(tokenConfig, "file:") {
		path := strings.TrimPrefix(tokenConfig, "file:")
		if path == "" {
			return "", fmt.Errorf("file path is empty")
		}
		source := NewFileTokenSource(path)
		return source.GetToken()
	}

	return "", fmt.Errorf("invalid token configuration: %s", tokenConfig)
}

// GenerateBenchmarkToken generates a system token for benchmark usage.
// It uses the existing TokenService from the identity package.
func GenerateBenchmarkToken(keyFile string, serviceName string, ttl time.Duration) (string, error) {
	// Create AuthN config with the specified key file and TTL
	cfg := config.AuthNConfig{
		AccessTokenTTL:  ttl,
		RefreshTokenTTL: ttl, // Not used for system tokens, but required
		AuthCodeTTL:     2 * time.Minute,
		PrivateKeyFile:  keyFile,
	}

	// Create token service
	tokenService, err := authn.NewTokenService(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to create token service: %w", err)
	}

	// Generate system token
	return tokenService.GenerateSystemToken(serviceName)
}
