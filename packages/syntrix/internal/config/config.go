package config

import (
	"log"
	"os"
	"path/filepath"

	"github.com/codetreker/syntrix/internal/core/database"
	storage "github.com/codetreker/syntrix/internal/core/storage/config"
	api "github.com/codetreker/syntrix/internal/gateway/config"
	indexer "github.com/codetreker/syntrix/internal/indexer/config"
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
	Deployment services.DeploymentConfig `yaml:"deployment"`
	Server     server.Config             `yaml:"server"`
	Logging    LoggingConfig             `yaml:"logging"`

	// Services
	Query    query.Config      `yaml:"query"`
	Indexer  indexer.Config    `yaml:"indexer"`
	Gateway  api.GatewayConfig `yaml:"gateway"`
	Trigger  trigger.Config    `yaml:"trigger"`
	Puller   puller.Config     `yaml:"puller"`
	Streamer streamer.Config   `yaml:"streamer"`

	// Components
	Storage  storage.Config  `yaml:"storage"`
	Identity IdentityConfig  `yaml:"identity"`
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
	// Determine config directory: parameter > env var > default
	if configDir == "" {
		configDir = os.Getenv("SYNTRIX_CONFIG_DIR")
	}
	if configDir == "" {
		configDir = DefaultConfigDir
	}

	// 1. Start with default values (so YAML can override them, including bool fields)
	cfg := &Config{
		Storage:    storage.DefaultConfig(),
		Identity:   DefaultIdentityConfig(),
		Server:     server.DefaultConfig(),
		Logging:    DefaultLoggingConfig(),
		Query:      query.DefaultConfig(),
		Gateway:    api.DefaultGatewayConfig(),
		Trigger:    trigger.DefaultConfig(),
		Deployment: services.DefaultDeploymentConfig(),
		Puller:     puller.DefaultConfig(),
		Streamer:   streamer.DefaultConfig(),
		Indexer:    indexer.DefaultConfig(),
		Database:   database.DefaultConfig(),
		DataDir:    DefaultDataDir,
		ConfigDir:  configDir,
	}

	// 2. Load config.yml (overrides defaults)
	loadFile(filepath.Join(configDir, "config.yml"), cfg)

	// 3. Load config.local.yml (overrides config.yml)
	loadFile(filepath.Join(configDir, "config.local.yml"), cfg)

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
		log.Fatalf("Configuration error: %v", err)
	}

	// Apply other configs with the now-known deployment mode
	if err := ApplyServiceConfigs(configDir, cfg.DataDir, cfg.Deployment.Mode,
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
		log.Fatalf("Configuration error: %v", err)
	}

	return cfg
}

func loadFile(filename string, cfg *Config) {
	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return // File doesn't exist, skip
		}
		log.Printf("Warning: Error reading %s: %v", filename, err)
		return
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		log.Printf("Warning: Error parsing %s: %v", filename, err)
	}
}
