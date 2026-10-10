package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestIdentityConfig_YAMLSectionAndOverlay(t *testing.T) {
	configDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yml"), []byte(`identity:
  authn:
    access_token_ttl: 20m
    private_key_file: keys/custom.pem
    password_policy:
      min_length: 16
      require_uppercase: false
      require_lowercase: false
  authz:
    rules_path: base_rules
  admin:
    username: operator
    password: configured-password
`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.local.yml"), []byte(`identity:
  authn:
    refresh_token_ttl: 48h
    password_policy:
      require_digit: false
  authz:
    rules_path: local_rules
`), 0600))

	cfg := LoadConfigFrom(configDir)
	assert.Equal(t, 20*time.Minute, cfg.Identity.AuthN.AccessTokenTTL)
	assert.Equal(t, 48*time.Hour, cfg.Identity.AuthN.RefreshTokenTTL)
	assert.Equal(t, 2*time.Minute, cfg.Identity.AuthN.AuthCodeTTL)
	assert.Equal(t, filepath.Join(configDir, "keys/custom.pem"), cfg.Identity.AuthN.PrivateKeyFile)
	assert.Equal(t, 16, cfg.Identity.AuthN.PasswordPolicy.MinLength)
	assert.False(t, cfg.Identity.AuthN.PasswordPolicy.RequireUppercase)
	assert.False(t, cfg.Identity.AuthN.PasswordPolicy.RequireLowercase)
	assert.False(t, cfg.Identity.AuthN.PasswordPolicy.RequireDigit)
	assert.True(t, cfg.Identity.AuthN.PasswordPolicy.RequireSpecial)
	assert.Equal(t, filepath.Join(configDir, "local_rules"), cfg.Identity.AuthZ.RulesPath)
	assert.Equal(t, "operator", cfg.Identity.Admin.Username)
	assert.Equal(t, "configured-password", cfg.Identity.Admin.Password)

	encoded, err := yaml.Marshal(cfg.Identity)
	require.NoError(t, err)
	var section map[string]any
	require.NoError(t, yaml.Unmarshal(encoded, &section))
	require.Len(t, section, 3)
	assert.Contains(t, section, "authn")
	assert.Contains(t, section, "authz")
	assert.Contains(t, section, "admin")
	assert.Equal(t, map[string]any{"rules_path": filepath.Join(configDir, "local_rules")}, section["authz"])
	var decoded IdentityConfig
	require.NoError(t, yaml.Unmarshal(encoded, &decoded))
	assert.Equal(t, cfg.Identity, decoded)
}

func TestIdentityConfig_Lifecycle(t *testing.T) {
	for _, mode := range []services.DeploymentMode{services.ModeStandalone, services.ModeDistributed} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := IdentityConfig{}
			require.EqualError(t, cfg.Validate(mode), "identity.authz.rules_path is required")
			cfg.ResolvePaths("configs", "data")
			assert.Empty(t, cfg.AuthN.PrivateKeyFile)
			assert.Empty(t, cfg.AuthZ.RulesPath)
			require.NoError(t, ApplyServiceConfigs("configs", "data", mode, &cfg))
			assert.Equal(t, 15*time.Minute, cfg.AuthN.AccessTokenTTL)
			assert.Equal(t, 7*24*time.Hour, cfg.AuthN.RefreshTokenTTL)
			assert.Equal(t, 2*time.Minute, cfg.AuthN.AuthCodeTTL)
			assert.Equal(t, filepath.Join("configs", "keys/auth_private.pem"), cfg.AuthN.PrivateKeyFile)
			assert.Equal(t, filepath.Join("configs", "security_rules"), cfg.AuthZ.RulesPath)
			assert.Equal(t, 12, cfg.AuthN.PasswordPolicy.MinLength)
			assert.False(t, cfg.AuthN.PasswordPolicy.RequireUppercase)
			assert.False(t, cfg.AuthN.PasswordPolicy.RequireLowercase)
			assert.False(t, cfg.AuthN.PasswordPolicy.RequireDigit)
			assert.False(t, cfg.AuthN.PasswordPolicy.RequireSpecial)
			assert.Equal(t, "syntrix", cfg.AuthN.AdminUsername)
			assert.Equal(t, "syntrix", cfg.Admin.Username)
		})
	}
}
