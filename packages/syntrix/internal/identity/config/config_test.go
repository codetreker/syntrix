package config

import (
	"path/filepath"
	"testing"
	"time"

	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// Verify AuthN defaults
	assert.Equal(t, 15*time.Minute, cfg.AuthN.AccessTokenTTL)
	assert.Equal(t, 7*24*time.Hour, cfg.AuthN.RefreshTokenTTL)
	assert.Equal(t, 2*time.Minute, cfg.AuthN.AuthCodeTTL)
	assert.Equal(t, "keys/auth_private.pem", cfg.AuthN.PrivateKeyFile)
}

func TestConfig_StructFields(t *testing.T) {
	cfg := Config{
		AuthN: AuthNConfig{
			AccessTokenTTL:  30 * time.Minute,
			RefreshTokenTTL: 24 * time.Hour,
			AuthCodeTTL:     5 * time.Minute,
			PrivateKeyFile:  "custom/key.pem",
		},
	}

	assert.Equal(t, 30*time.Minute, cfg.AuthN.AccessTokenTTL)
	assert.Equal(t, 24*time.Hour, cfg.AuthN.RefreshTokenTTL)
	assert.Equal(t, 5*time.Minute, cfg.AuthN.AuthCodeTTL)
	assert.Equal(t, "custom/key.pem", cfg.AuthN.PrivateKeyFile)
}

func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()

	assert.Equal(t, 15*time.Minute, cfg.AuthN.AccessTokenTTL)
	assert.Equal(t, 7*24*time.Hour, cfg.AuthN.RefreshTokenTTL)
	assert.Equal(t, 2*time.Minute, cfg.AuthN.AuthCodeTTL)
	assert.Equal(t, "keys/auth_private.pem", cfg.AuthN.PrivateKeyFile)
}

func TestConfig_ApplyEnvOverrides(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ApplyEnvOverrides()
	// No env vars, just verify no panic
}

func TestConfig_ResolvePaths(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ResolvePaths("base", "data")

	assert.Equal(t, filepath.Join("base", "keys/auth_private.pem"), cfg.AuthN.PrivateKeyFile)
}

func TestConfig_Validate(t *testing.T) {
	cfg := DefaultConfig()
	err := cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}

func TestConfig_ApplyDefaults_CustomValuesPreserved(t *testing.T) {
	cfg := &Config{
		AuthN: AuthNConfig{
			AccessTokenTTL:  30 * time.Minute,
			RefreshTokenTTL: 14 * 24 * time.Hour,
			AuthCodeTTL:     5 * time.Minute,
			PrivateKeyFile:  "custom/key.pem",
		},
	}
	cfg.ApplyDefaults()

	assert.Equal(t, 30*time.Minute, cfg.AuthN.AccessTokenTTL)
	assert.Equal(t, 14*24*time.Hour, cfg.AuthN.RefreshTokenTTL)
	assert.Equal(t, 5*time.Minute, cfg.AuthN.AuthCodeTTL)
	assert.Equal(t, "custom/key.pem", cfg.AuthN.PrivateKeyFile)
}

func TestConfig_ApplyDefaults_PartialConfig(t *testing.T) {
	cfg := &Config{
		AuthN: AuthNConfig{
			AccessTokenTTL: 60 * time.Minute,
			// Other fields empty, should get defaults
		},
	}
	cfg.ApplyDefaults()

	assert.Equal(t, 60*time.Minute, cfg.AuthN.AccessTokenTTL)
	assert.Equal(t, 7*24*time.Hour, cfg.AuthN.RefreshTokenTTL)
	assert.Equal(t, 2*time.Minute, cfg.AuthN.AuthCodeTTL)
	assert.Equal(t, "keys/auth_private.pem", cfg.AuthN.PrivateKeyFile)
}

func TestConfig_ResolvePaths_EmptyPaths(t *testing.T) {
	cfg := Config{
		AuthN: AuthNConfig{
			PrivateKeyFile: "",
		},
	}
	cfg.ResolvePaths("/config", "/data")

	// Empty paths should stay empty
	assert.Equal(t, "", cfg.AuthN.PrivateKeyFile)
}

func TestConfig_ResolvePaths_PrivateKeyAbsolute(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AuthN.PrivateKeyFile = "/absolute/key.pem"
	cfg.ResolvePaths("config", "data")

	assert.Equal(t, "/absolute/key.pem", cfg.AuthN.PrivateKeyFile)
}

func TestConfig_Validate_EmptyConfig(t *testing.T) {
	cfg := Config{}
	err := cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}

func TestCollectionTopologyDefaults(t *testing.T) {
	userDefaults := DefaultUserTopology()
	assert.Equal(t, CollectionTopology{BaseTopology: BaseTopology{Strategy: "single", Primary: "default_postgres"}, Collection: "auth_users"}, userDefaults)
	revocationDefaults := DefaultRevocationTopology()
	assert.Equal(t, CollectionTopology{BaseTopology: BaseTopology{Strategy: "single", Primary: "default_mongo"}, Collection: "revocations"}, revocationDefaults)
	for _, defaults := range []CollectionTopology{userDefaults, revocationDefaults} {
		for _, tc := range []struct {
			name    string
			initial CollectionTopology
			want    CollectionTopology
		}{
			{name: "empty", want: defaults},
			{name: "partial", initial: CollectionTopology{BaseTopology: BaseTopology{Strategy: "read_write_split", Replica: "replica"}}, want: CollectionTopology{BaseTopology: BaseTopology{Strategy: "read_write_split", Primary: defaults.Primary, Replica: "replica"}, Collection: defaults.Collection}},
			{name: "explicit", initial: CollectionTopology{BaseTopology: BaseTopology{Strategy: "custom", Primary: "primary", Replica: "replica"}, Collection: "custom"}, want: CollectionTopology{BaseTopology: BaseTopology{Strategy: "custom", Primary: "primary", Replica: "replica"}, Collection: "custom"}},
		} {
			t.Run(tc.name+defaults.Collection, func(t *testing.T) { cfg := tc.initial; cfg.ApplyDefaults(defaults); assert.Equal(t, tc.want, cfg) })
		}
	}
}
