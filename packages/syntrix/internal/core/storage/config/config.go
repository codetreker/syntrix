package config

import (
	"fmt"
	"os"
	"time"

	services "github.com/codetreker/syntrix/internal/services/config"
)

type Config struct {
	Backends  map[string]BackendConfig  `yaml:"backends"`
	Topology  TopologyConfig            `yaml:"topology"`
	Databases map[string]DatabaseConfig `yaml:"databases"`
}

type DatabaseConfig struct {
	Backend string `yaml:"backend"`
}

type BackendConfig struct {
	Type     string         `yaml:"type"` // "mongo" or "postgres"
	Mongo    MongoConfig    `yaml:"mongo"`
	Postgres PostgresConfig `yaml:"postgres"`
}

type TopologyConfig struct {
	Document   DocumentTopology   `yaml:"document"`
	User       CollectionTopology `yaml:"user"`
	Revocation CollectionTopology `yaml:"revocation"`
}

type BaseTopology struct {
	Strategy string `yaml:"strategy"` // "single", "read_write_split"
	Primary  string `yaml:"primary"`
	Replica  string `yaml:"replica"`
}

type DocumentTopology struct {
	BaseTopology        `yaml:",inline"`
	DataCollection      string        `yaml:"data_collection"`
	SysCollection       string        `yaml:"sys_collection"`
	SoftDeleteRetention time.Duration `yaml:"soft_delete_retention"`
}

type CollectionTopology struct {
	BaseTopology `yaml:",inline"`
	Collection   string `yaml:"collection"`
}

type MongoConfig struct {
	URI          string `yaml:"uri"`
	DatabaseName string `yaml:"database_name"`
}

type PostgresConfig struct {
	DSN             string        `yaml:"dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
}

func DefaultConfig() Config {
	return Config{
		Backends: map[string]BackendConfig{
			"default_mongo": {
				Type: "mongo",
				Mongo: MongoConfig{
					URI:          "mongodb://localhost:27017",
					DatabaseName: "syntrix",
				},
			},
			"default_postgres": {
				Type: "postgres",
				Postgres: PostgresConfig{
					DSN:             "postgres://syntrix:syntrix@localhost:5432/syntrix?sslmode=disable",
					MaxOpenConns:    10,
					MaxIdleConns:    5,
					ConnMaxLifetime: 5 * time.Minute,
				},
			},
		},
		Topology: TopologyConfig{
			Document: DocumentTopology{
				BaseTopology: BaseTopology{
					Strategy: "single",
					Primary:  "default_mongo",
				},
				DataCollection:      "documents",
				SysCollection:       "sys",
				SoftDeleteRetention: 5 * time.Minute,
			},
			User: CollectionTopology{
				BaseTopology: BaseTopology{
					Strategy: "single",
					Primary:  "default_postgres",
				},
				Collection: "auth_users",
			},
			Revocation: CollectionTopology{
				BaseTopology: BaseTopology{
					Strategy: "single",
					Primary:  "default_mongo",
				},
				Collection: "revocations",
			},
		},
		Databases: map[string]DatabaseConfig{
			"default": {
				Backend: "default_mongo",
			},
		},
	}
}

func (c *Config) Validate(_ services.DeploymentMode) error {
	// Validate Storage Databases
	if _, ok := c.Databases["default"]; !ok {
		return fmt.Errorf("storage.databases.default is required")
	}
	for tID, tCfg := range c.Databases {
		if _, ok := c.Backends[tCfg.Backend]; !ok {
			return fmt.Errorf("database '%s' references unknown backend '%s'", tID, tCfg.Backend)
		}
	}

	return nil
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.Backends == nil {
		c.Backends = defaults.Backends
	}
	if c.Databases == nil {
		c.Databases = defaults.Databases
	}
	// Apply topology defaults
	if c.Topology.Document.Strategy == "" {
		c.Topology.Document.Strategy = defaults.Topology.Document.Strategy
	}
	if c.Topology.Document.Primary == "" {
		c.Topology.Document.Primary = defaults.Topology.Document.Primary
	}
	if c.Topology.Document.DataCollection == "" {
		c.Topology.Document.DataCollection = defaults.Topology.Document.DataCollection
	}
	if c.Topology.Document.SysCollection == "" {
		c.Topology.Document.SysCollection = defaults.Topology.Document.SysCollection
	}
	if c.Topology.Document.SoftDeleteRetention == 0 {
		c.Topology.Document.SoftDeleteRetention = defaults.Topology.Document.SoftDeleteRetention
	}
	if c.Topology.User.Strategy == "" {
		c.Topology.User.Strategy = defaults.Topology.User.Strategy
	}
	if c.Topology.User.Primary == "" {
		c.Topology.User.Primary = defaults.Topology.User.Primary
	}
	if c.Topology.User.Collection == "" {
		c.Topology.User.Collection = defaults.Topology.User.Collection
	}
	if c.Topology.Revocation.Strategy == "" {
		c.Topology.Revocation.Strategy = defaults.Topology.Revocation.Strategy
	}
	if c.Topology.Revocation.Primary == "" {
		c.Topology.Revocation.Primary = defaults.Topology.Revocation.Primary
	}
	if c.Topology.Revocation.Collection == "" {
		c.Topology.Revocation.Collection = defaults.Topology.Revocation.Collection
	}
}

// ApplyEnvOverrides applies environment variable overrides.
func (c *Config) ApplyEnvOverrides() {
	if val := os.Getenv("MONGO_URI"); val != "" {
		if backend, ok := c.Backends["default_mongo"]; ok {
			backend.Mongo.URI = val
			c.Backends["default_mongo"] = backend
		}
	}
	if val := os.Getenv("DB_NAME"); val != "" {
		if backend, ok := c.Backends["default_mongo"]; ok {
			backend.Mongo.DatabaseName = val
			c.Backends["default_mongo"] = backend
		}
	}
}

// ResolvePaths resolves relative paths using the given directories.
// No paths to resolve in storage config.
func (c *Config) ResolvePaths(_, _ string) { _ = c }
