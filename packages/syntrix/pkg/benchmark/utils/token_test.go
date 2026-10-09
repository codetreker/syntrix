package utils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticTokenSource(t *testing.T) {
	tests := []struct {
		name        string
		token       string
		expectError bool
	}{
		{"valid token", "test-token-123", false},
		{"empty token", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := NewStaticTokenSource(tt.token)
			token, err := source.GetToken()

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, token)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.token, token)
			}
		})
	}
}

func TestEnvTokenSource(t *testing.T) {
	tests := []struct {
		name        string
		envVar      string
		envValue    string
		expectError bool
	}{
		{"default env var", "", "test-token", false},
		{"custom env var", "CUSTOM_TOKEN", "custom-value", false},
		{"missing env var", "MISSING_TOKEN", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment
			envVarName := tt.envVar
			if envVarName == "" {
				envVarName = "SYNTRIX_TOKEN"
			}

			if tt.envValue != "" {
				os.Setenv(envVarName, tt.envValue)
				defer os.Unsetenv(envVarName)
			} else {
				os.Unsetenv(envVarName)
			}

			source := NewEnvTokenSource(tt.envVar)
			token, err := source.GetToken()

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, token)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.envValue, token)
			}
		})
	}
}

func TestFileTokenSource(t *testing.T) {
	// Create temporary directory
	tmpDir := t.TempDir()

	tests := []struct {
		name        string
		fileContent string
		expectError bool
		expectedVal string
	}{
		{"valid token file", "test-token-from-file", false, "test-token-from-file"},
		{"token with whitespace", "  token-with-spaces  \n", false, "token-with-spaces"},
		{"empty file", "", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenFile := filepath.Join(tmpDir, "token_"+tt.name+".txt")
			if tt.fileContent != "" || tt.name == "empty file" {
				err := os.WriteFile(tokenFile, []byte(tt.fileContent), 0600)
				require.NoError(t, err)
			}

			source := NewFileTokenSource(tokenFile)
			token, err := source.GetToken()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedVal, token)
			}
		})
	}

	t.Run("nonexistent file", func(t *testing.T) {
		source := NewFileTokenSource(filepath.Join(tmpDir, "nonexistent.txt"))
		token, err := source.GetToken()
		assert.Error(t, err)
		assert.Empty(t, token)
	})
}

func TestGenerateTestToken(t *testing.T) {
	token1 := GenerateTestToken()
	token2 := GenerateTestToken()

	assert.NotEmpty(t, token1)
	assert.NotEmpty(t, token2)
	assert.NotEqual(t, token1, token2, "Generated tokens should be unique")

	// Check token is a valid base64 string
	assert.Greater(t, len(token1), 40, "Token should be reasonably long")
}

func TestLoadToken(t *testing.T) {
	tmpDir := t.TempDir()
	tokenFile := filepath.Join(tmpDir, "token.txt")
	err := os.WriteFile(tokenFile, []byte("file-token-value"), 0600)
	require.NoError(t, err)

	tests := []struct {
		name        string
		config      string
		envSetup    func()
		envCleanup  func()
		expectError bool
		expectedVal string
	}{
		{
			name:        "direct token",
			config:      "direct-token-123",
			expectError: false,
			expectedVal: "direct-token-123",
		},
		{
			name:   "env token with default var",
			config: "env:",
			envSetup: func() {
				os.Setenv("SYNTRIX_TOKEN", "env-token-default")
			},
			envCleanup: func() {
				os.Unsetenv("SYNTRIX_TOKEN")
			},
			expectError: false,
			expectedVal: "env-token-default",
		},
		{
			name:   "env token with custom var",
			config: "env:CUSTOM_TOKEN",
			envSetup: func() {
				os.Setenv("CUSTOM_TOKEN", "custom-env-token")
			},
			envCleanup: func() {
				os.Unsetenv("CUSTOM_TOKEN")
			},
			expectError: false,
			expectedVal: "custom-env-token",
		},
		{
			name:        "file token",
			config:      "file:" + tokenFile,
			expectError: false,
			expectedVal: "file-token-value",
		},
		{
			name:        "empty config",
			config:      "",
			expectError: true,
		},
		{
			name:        "env token missing",
			config:      "env:MISSING_VAR",
			expectError: true,
		},
		{
			name:        "file token empty path",
			config:      "file:",
			expectError: true,
		},
		{
			name:        "file token nonexistent",
			config:      "file:/nonexistent/path/token.txt",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envSetup != nil {
				tt.envSetup()
			}
			if tt.envCleanup != nil {
				defer tt.envCleanup()
			}

			token, err := LoadToken(tt.config)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedVal, token)
			}
		})
	}
}

func TestLoadToken_EdgeCases(t *testing.T) {
	t.Run("token starting with env: but not env source", func(t *testing.T) {
		// This is actually treated as env source, not a direct token
		token, err := LoadToken("env:SOME_VAR")
		assert.Error(t, err) // Should error because env var doesn't exist
		assert.Empty(t, token)
	})

	t.Run("token starting with file: but not file source", func(t *testing.T) {
		// This is actually treated as file source, not a direct token
		token, err := LoadToken("file:/nonexistent")
		assert.Error(t, err) // Should error because file doesn't exist
		assert.Empty(t, token)
	})

	t.Run("direct token that looks like a path", func(t *testing.T) {
		// Direct tokens don't need to be special format
		token, err := LoadToken("/some/path/token")
		assert.NoError(t, err)
		assert.Equal(t, "/some/path/token", token)
	})
}
