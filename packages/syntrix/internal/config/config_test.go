package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	services_config "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestLoadConfig_Defaults(t *testing.T) {
	// Ensure no env vars interfere
	os.Unsetenv("MONGO_URI")
	os.Unsetenv("DB_NAME")
	os.Unsetenv("GATEWAY_PORT")

	cfg := LoadConfig()

	assert.Equal(t, "mongodb://localhost:27017", cfg.Storage.Backends["default_mongo"].Mongo.URI)
	assert.Equal(t, "syntrix", cfg.Storage.Backends["default_mongo"].Mongo.DatabaseName)
}

func TestLoadConfig_EnvVars(t *testing.T) {
	os.Setenv("MONGO_URI", "mongodb://test:27017")
	os.Setenv("DB_NAME", "testdb")
	os.Setenv("GATEWAY_QUERY_SERVICE_URL", "http://api-env")
	os.Setenv("TRIGGER_NATS_URL", "nats://env:4222")
	os.Setenv("TRIGGER_RULES_PATH", "custom")
	defer func() {
		os.Unsetenv("MONGO_URI")
		os.Unsetenv("DB_NAME")
		os.Unsetenv("GATEWAY_QUERY_SERVICE_URL")
		os.Unsetenv("TRIGGER_NATS_URL")
		os.Unsetenv("TRIGGER_RULES_PATH")
	}()

	cfg := LoadConfig()

	assert.Equal(t, "mongodb://test:27017", cfg.Storage.Backends["default_mongo"].Mongo.URI)
	assert.Equal(t, "testdb", cfg.Storage.Backends["default_mongo"].Mongo.DatabaseName)
	assert.Equal(t, "http://api-env", cfg.Gateway.QueryServiceURL)
	assert.Equal(t, "nats://env:4222", cfg.Trigger.NatsURL)
	assert.True(t, strings.HasSuffix(cfg.Trigger.Evaluator.RulesPath, filepath.Join("configs", "custom")))
}

func TestLoadConfig_LoadFileErrors(t *testing.T) {
	require.NoError(t, os.Mkdir("configs", 0755))
	defer os.RemoveAll("configs")

	// Create a directory where a file is expected to trigger read error path
	require.NoError(t, os.Mkdir("configs/config.yml", 0755))

	// Malformed YAML to trigger parse error path
	require.NoError(t, os.WriteFile("configs/config.local.yml", []byte("not: [valid"), 0644))

	cfg := LoadConfig()

	// Defaults should remain when files fail to load/parse
	assert.Equal(t, 8080, cfg.Server.HTTPPort)
	assert.Equal(t, "mongodb://localhost:27017", cfg.Storage.Backends["default_mongo"].Mongo.URI)
}

func TestLoadConfig_File(t *testing.T) {
	err := os.Mkdir("configs", 0755)
	require.NoError(t, err)
	defer os.RemoveAll("configs")

	// Create a temporary config.yml in the config directory
	configContent := []byte(`
storage:
  backends:
    default_mongo:
      mongo:
        uri: "mongodb://file:27017"
        database_name: "filedb"
gateway:
  port: 7070
`)
	err = os.WriteFile("configs/config.yml", configContent, 0644)
	require.NoError(t, err)

	cfg := LoadConfig()

	assert.Equal(t, "mongodb://file:27017", cfg.Storage.Backends["default_mongo"].Mongo.URI)
	assert.Equal(t, "filedb", cfg.Storage.Backends["default_mongo"].Mongo.DatabaseName)
}

func TestLoadConfig_ModuleSectionsAndOverlay(t *testing.T) {
	for _, mode := range []services_config.DeploymentMode{services_config.ModeStandalone, services_config.ModeDistributed} {
		t.Run(string(mode), func(t *testing.T) {
			configDir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yml"), []byte(`deployment:
  mode: `+string(mode)+`
identity:
  authn:
    access_token_ttl: 20m
    private_key_file: keys/custom.pem
    password_policy:
      min_length: 16
      require_uppercase: false
      require_lowercase: false
  admin:
    username: operator
    password: configured-password
gateway:
  authz:
    rules_path: base_rules
`), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.local.yml"), []byte(`identity:
  authn:
    refresh_token_ttl: 48h
    password_policy:
      require_digit: false
gateway:
  authz:
    rules_path: local_rules
`), 0600))

			cfg := LoadConfigFrom(configDir)
			assert.Equal(t, mode, cfg.Deployment.Mode)
			assert.Equal(t, 20*time.Minute, cfg.Identity.AuthN.AccessTokenTTL)
			assert.Equal(t, 48*time.Hour, cfg.Identity.AuthN.RefreshTokenTTL)
			assert.Equal(t, 2*time.Minute, cfg.Identity.AuthN.AuthCodeTTL)
			assert.Equal(t, filepath.Join(configDir, "keys/custom.pem"), cfg.Identity.AuthN.PrivateKeyFile)
			assert.Equal(t, 16, cfg.Identity.AuthN.PasswordPolicy.MinLength)
			assert.False(t, cfg.Identity.AuthN.PasswordPolicy.RequireUppercase)
			assert.False(t, cfg.Identity.AuthN.PasswordPolicy.RequireLowercase)
			assert.False(t, cfg.Identity.AuthN.PasswordPolicy.RequireDigit)
			assert.True(t, cfg.Identity.AuthN.PasswordPolicy.RequireSpecial)
			assert.Equal(t, filepath.Join(configDir, "local_rules"), cfg.Gateway.AuthZ.RulesPath)
			assert.Equal(t, "operator", cfg.Identity.Admin.Username)
			assert.Equal(t, "configured-password", cfg.Identity.Admin.Password)

			encoded, err := yaml.Marshal(map[string]any{"identity": cfg.Identity, "gateway": cfg.Gateway})
			require.NoError(t, err)
			var sections map[string]any
			require.NoError(t, yaml.Unmarshal(encoded, &sections))
			identitySection, ok := sections["identity"].(map[string]any)
			require.True(t, ok)
			require.Len(t, identitySection, 2)
			assert.Contains(t, identitySection, "authn")
			assert.Contains(t, identitySection, "admin")
			gatewaySection, ok := sections["gateway"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, map[string]any{"rules_path": filepath.Join(configDir, "local_rules")}, gatewaySection["authz"])
			var decoded Config
			require.NoError(t, yaml.Unmarshal(encoded, &decoded))
			assert.Equal(t, cfg.Identity, decoded.Identity)
			assert.Equal(t, cfg.Gateway, decoded.Gateway)
		})
	}
}

func TestDeploymentMode_IsStandalone_ViaConfig(t *testing.T) {
	cfg := &Config{Deployment: services_config.DeploymentConfig{Mode: services_config.ModeStandalone}}
	assert.True(t, cfg.Deployment.Mode.IsStandalone())

	cfg = &Config{Deployment: services_config.DeploymentConfig{Mode: services_config.ModeDistributed}}
	assert.False(t, cfg.Deployment.Mode.IsStandalone())

	cfg = &Config{Deployment: services_config.DeploymentConfig{Mode: ""}}
	assert.False(t, cfg.Deployment.Mode.IsStandalone())
}

// Note: Config.Validate() has been removed. Mode-dependent validation is now
// performed by individual service configs via their Validate(mode) method.
// Tests for distributed mode address requirements are in the respective
// service config test files (e.g., query/config/config_test.go,
// gateway/config/config_test.go, etc.)

func TestLoadConfigFrom_CustomDir(t *testing.T) {
	// Create a custom config directory
	customDir := t.TempDir()
	configContent := []byte(`
server:
  http_port: 9999
storage:
  backends:
    default_mongo:
      mongo:
        uri: "mongodb://custom:27017"
        database_name: "customdb"
`)
	err := os.WriteFile(filepath.Join(customDir, "config.yml"), configContent, 0644)
	require.NoError(t, err)

	cfg := LoadConfigFrom(customDir)

	assert.Equal(t, customDir, cfg.ConfigDir)
	assert.Equal(t, 9999, cfg.Server.HTTPPort)
	assert.Equal(t, "mongodb://custom:27017", cfg.Storage.Backends["default_mongo"].Mongo.URI)
	assert.Equal(t, "customdb", cfg.Storage.Backends["default_mongo"].Mongo.DatabaseName)
}

func TestLoadConfigFrom_EnvVar(t *testing.T) {
	// Create a custom config directory
	customDir := t.TempDir()
	configContent := []byte(`
server:
  http_port: 8888
`)
	err := os.WriteFile(filepath.Join(customDir, "config.yml"), configContent, 0644)
	require.NoError(t, err)

	// Set env var
	os.Setenv("SYNTRIX_CONFIG_DIR", customDir)
	defer os.Unsetenv("SYNTRIX_CONFIG_DIR")

	// Empty string should fall back to env var
	cfg := LoadConfigFrom("")

	assert.Equal(t, customDir, cfg.ConfigDir)
	assert.Equal(t, 8888, cfg.Server.HTTPPort)
}

func TestLoadConfigFrom_ParameterOverridesEnvVar(t *testing.T) {
	// Create two config directories
	envDir := t.TempDir()
	paramDir := t.TempDir()

	// Env config
	err := os.WriteFile(filepath.Join(envDir, "config.yml"), []byte(`server:
  http_port: 1111
`), 0644)
	require.NoError(t, err)

	// Param config
	err = os.WriteFile(filepath.Join(paramDir, "config.yml"), []byte(`server:
  http_port: 2222
`), 0644)
	require.NoError(t, err)

	// Set env var
	os.Setenv("SYNTRIX_CONFIG_DIR", envDir)
	defer os.Unsetenv("SYNTRIX_CONFIG_DIR")

	// Parameter should override env var
	cfg := LoadConfigFrom(paramDir)

	assert.Equal(t, paramDir, cfg.ConfigDir)
	assert.Equal(t, 2222, cfg.Server.HTTPPort)
}

func TestLoadConfigFrom_DefaultFallback(t *testing.T) {
	// Clear env var to ensure default is used
	os.Unsetenv("SYNTRIX_CONFIG_DIR")

	cfg := LoadConfigFrom("")

	assert.Equal(t, DefaultConfigDir, cfg.ConfigDir)
}
