package config

import (
	"github.com/codetreker/syntrix/internal/core/origin"
	"os"
	"path/filepath"

	"github.com/codetreker/syntrix/internal/core/database"
	storage "github.com/codetreker/syntrix/internal/core/storage/config"
	api "github.com/codetreker/syntrix/internal/gateway/config"
	identity "github.com/codetreker/syntrix/internal/identity/config"
	indexer "github.com/codetreker/syntrix/internal/indexer/config"
	initialization "github.com/codetreker/syntrix/internal/initialization/config"
	puller "github.com/codetreker/syntrix/internal/puller/config"
	query "github.com/codetreker/syntrix/internal/query/config"
	server "github.com/codetreker/syntrix/internal/server"
	services "github.com/codetreker/syntrix/internal/services/config"
	streamer "github.com/codetreker/syntrix/internal/streamer"
	trigger "github.com/codetreker/syntrix/internal/trigger/config"
	"gopkg.in/yaml.v3"
)

// Config holds the application configuration
type Config struct {
	Deployment     services.DeploymentConfig `yaml:"deployment"`
	Services       services.Config           `yaml:"services"`
	Initialization initialization.Config     `yaml:"initialization"`
	Server         server.Config             `yaml:"server"`
	Logging        LoggingConfig             `yaml:"logging"`

	// Services
	Query    query.Config      `yaml:"query"`
	Indexer  indexer.Config    `yaml:"indexer"`
	Gateway  api.GatewayConfig `yaml:"gateway"`
	Trigger  trigger.Config    `yaml:"trigger"`
	Puller   puller.Config     `yaml:"puller"`
	Streamer streamer.Config   `yaml:"streamer"`

	// Components
	Storage  storage.Config  `yaml:"storage"`
	Identity identity.Config `yaml:"identity"`
	Database database.Config `yaml:"database"`

	// DataDir is the root directory for all runtime data (logs, caches, indexes, etc.)
	// Relative paths are resolved from the current working directory.
	// Defaults to "data".
	DataDir string `yaml:"data_dir"`

	// ConfigDir is the directory where config files are loaded from (not serialized to YAML)
	ConfigDir string `yaml:"-"`
}

// DefaultConfigDir is the default configuration directory
const DefaultConfigDir = "configs"

// DefaultDataDir is the default data directory
const DefaultDataDir = ".syntrix/data"

// LoadConfig loads configuration from the default directory.
// Use LoadConfigFrom to specify a custom directory.
func LoadConfig() *Config {
	return LoadConfigFrom("")
}

// LoadConfigFrom loads configuration from the specified directory.
// If configDir is empty, it checks SYNTRIX_CONFIG_DIR env var, then falls back to "configs".
// Order: defaults -> config.yml -> config.local.yml -> ApplyEnvOverrides -> ResolvePaths -> Validate
func LoadConfigFrom(configDir string) *Config {
	cfg, err := ReadConfigFrom(configDir)
	if err != nil {
		panic(err)
	}
	return cfg
}

func ReadConfigFrom(configDir string) (*Config, error) {
	// Determine config directory: parameter > env var > default
	if configDir == "" {
		configDir = os.Getenv("SYNTRIX_CONFIG_DIR")
	}
	if configDir == "" {
		configDir = DefaultConfigDir
	}

	// 1. Start with default values (so YAML can override them, including bool fields)
	cfg := DefaultConfig()
	cfg.ConfigDir = configDir

	if configFileExists(filepath.Join(configDir, "config.yml")) || configFileExists(filepath.Join(configDir, "config.local.yml")) {
		cfg.Trigger.PrepareForLoad()
	}

	// 2. Load config.yml (overrides defaults)
	if err := loadFile(filepath.Join(configDir, "config.yml"), cfg); err != nil {
		return nil, err
	}

	// 3. Load config.local.yml (overrides config.yml)
	if err := loadFile(filepath.Join(configDir, "config.local.yml"), cfg); err != nil {
		return nil, err
	}

	// 4. Apply data_dir from environment variable if set
	if val := os.Getenv("SYNTRIX_DATA_DIR"); val != "" {
		cfg.DataDir = val
	}

	// 5. Apply configuration lifecycle: ApplyDefaults fills gaps, ApplyEnvOverrides, ResolvePaths, Validate
	// Note: We need to process Deployment first to get the mode, then pass it to other configs
	cfg.Deployment.ApplyDefaults()
	cfg.Deployment.ApplyEnvOverrides()
	cfg.Deployment.ResolvePaths(configDir, cfg.DataDir)
	if err := cfg.Deployment.Validate(cfg.Deployment.Mode); err != nil {
		return nil, &LoadError{Phase: "validation", Cause: err}
	}

	// Apply other configs with the now-known deployment mode
	if err := ApplyServiceConfigs(configDir, cfg.DataDir, cfg.Deployment.Mode,
		&cfg.Services,
		&cfg.Initialization,
		&cfg.Server,
		&cfg.Logging,
		&cfg.Query,
		&cfg.Indexer,
		&cfg.Gateway,
		&cfg.Trigger,
		&cfg.Puller,
		&cfg.Streamer,
		&cfg.Storage,
		&cfg.Identity,
	); err != nil {
		return nil, &LoadError{Phase: "validation", Cause: err}
	}

	if err := services.ValidateOrigins(cfg.Initialization.Mode == initialization.Managed, cfg.Server.EnableCORS,
		origin.Policy{AllowedOrigins: cfg.Server.AllowedOrigins, AllowDevelopment: cfg.Server.AllowDevOrigin},
		origin.Policy{AllowedOrigins: cfg.Gateway.Realtime.AllowedOrigins, AllowDevelopment: cfg.Gateway.Realtime.AllowDevOrigin}); err != nil {
		return nil, &LoadError{Phase: "validation", Cause: err}
	}
	return cfg, nil
}

type LoadError struct {
	Phase string
	Cause error
}

func (e *LoadError) Error() string { return "configuration " + e.Phase + " failed" }
func (e *LoadError) Unwrap() error { return e.Cause }

func loadFile(filename string, cfg *Config) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &LoadError{Phase: "read", Cause: err}
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return &LoadError{Phase: "decode", Cause: err}
	}
	return nil
}

func DefaultConfig() *Config {
	return &Config{
		Storage:        storage.DefaultConfig(),
		Services:       services.DefaultConfig(),
		Initialization: initialization.DefaultConfig(),
		Identity:       identity.DefaultConfig(),
		Server:         server.DefaultConfig(),
		Logging:        DefaultLoggingConfig(),
		Query:          query.DefaultConfig(),
		Gateway:        api.DefaultGatewayConfig(),
		Trigger:        trigger.DefaultConfig(),
		Deployment:     services.DefaultDeploymentConfig(),
		Puller:         puller.DefaultConfig(),
		Streamer:       streamer.DefaultConfig(),
		Indexer:        indexer.DefaultConfig(),
		Database:       database.DefaultConfig(),
		DataDir:        DefaultDataDir,
		ConfigDir:      DefaultConfigDir,
	}

}

func configFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
