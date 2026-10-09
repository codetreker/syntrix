package config

import (
	"errors"
	"path/filepath"
	"time"

	services "github.com/codetreker/syntrix/internal/services/config"
)

type Config struct {
	AuthN AuthNConfig `yaml:"authn"`
	AuthZ AuthZConfig `yaml:"authz"`
	Admin AdminConfig `yaml:"admin"`
}

type AuthNConfig struct {
	AccessTokenTTL  time.Duration        `yaml:"access_token_ttl"`
	RefreshTokenTTL time.Duration        `yaml:"refresh_token_ttl"`
	AuthCodeTTL     time.Duration        `yaml:"auth_code_ttl"`
	PrivateKeyFile  string               `yaml:"private_key_file"`
	PasswordPolicy  PasswordPolicyConfig `yaml:"password_policy"`
	AdminUsername   string               `yaml:"admin_username"` // Username that receives admin role on signup
}

// PasswordPolicyConfig defines password complexity requirements.
type PasswordPolicyConfig struct {
	MinLength        int  `yaml:"min_length"`
	RequireUppercase bool `yaml:"require_uppercase"`
	RequireLowercase bool `yaml:"require_lowercase"`
	RequireDigit     bool `yaml:"require_digit"`
	RequireSpecial   bool `yaml:"require_special"`
}

type AuthZConfig struct {
	RulesPath string `yaml:"rules_path"`
}

type AdminConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func DefaultConfig() Config {
	return Config{
		AuthN: AuthNConfig{
			AccessTokenTTL:  15 * time.Minute,
			RefreshTokenTTL: 7 * 24 * time.Hour,
			AuthCodeTTL:     2 * time.Minute,
			PrivateKeyFile:  "keys/auth_private.pem",
			AdminUsername:   "syntrix", // Default admin username, configurable
			PasswordPolicy: PasswordPolicyConfig{
				MinLength:        12,
				RequireUppercase: true,
				RequireLowercase: true,
				RequireDigit:     true,
				RequireSpecial:   true,
			},
		},
		AuthZ: AuthZConfig{
			RulesPath: "security_rules",
		},
		Admin: AdminConfig{
			Username: "syntrix",
			Password: "", // Must be set in config file
		},
	}
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.AuthN.AccessTokenTTL == 0 {
		c.AuthN.AccessTokenTTL = defaults.AuthN.AccessTokenTTL
	}
	if c.AuthN.RefreshTokenTTL == 0 {
		c.AuthN.RefreshTokenTTL = defaults.AuthN.RefreshTokenTTL
	}
	if c.AuthN.AuthCodeTTL == 0 {
		c.AuthN.AuthCodeTTL = defaults.AuthN.AuthCodeTTL
	}
	if c.AuthN.PrivateKeyFile == "" {
		c.AuthN.PrivateKeyFile = defaults.AuthN.PrivateKeyFile
	}
	if c.AuthN.PasswordPolicy.MinLength == 0 {
		c.AuthN.PasswordPolicy.MinLength = defaults.AuthN.PasswordPolicy.MinLength
	}
	if c.AuthN.AdminUsername == "" {
		c.AuthN.AdminUsername = defaults.AuthN.AdminUsername
	}
	if c.AuthZ.RulesPath == "" {
		c.AuthZ.RulesPath = defaults.AuthZ.RulesPath
	}
	if c.Admin.Username == "" {
		c.Admin.Username = defaults.Admin.Username
	}
}

// ApplyEnvOverrides applies environment variable overrides.
// No env vars for identity config currently.
func (c *Config) ApplyEnvOverrides() { _ = c }

// ResolvePaths resolves relative paths using the given directories.
// - configDir: base directory for config-related paths (rules_path, private_key_file)
// - dataDir: not used for identity config
func (c *Config) ResolvePaths(configDir, dataDir string) {
	_ = dataDir // identity has no data-related paths
	if c.AuthZ.RulesPath != "" && !filepath.IsAbs(c.AuthZ.RulesPath) {
		c.AuthZ.RulesPath = filepath.Join(configDir, c.AuthZ.RulesPath)
	}
	if c.AuthN.PrivateKeyFile != "" && !filepath.IsAbs(c.AuthN.PrivateKeyFile) {
		c.AuthN.PrivateKeyFile = filepath.Join(configDir, c.AuthN.PrivateKeyFile)
	}
}

// Validate returns an error if the configuration is invalid.
func (c *Config) Validate(_ services.DeploymentMode) error {
	if c.AuthZ.RulesPath == "" {
		return errors.New("identity.authz.rules_path is required")
	}
	return nil
}
