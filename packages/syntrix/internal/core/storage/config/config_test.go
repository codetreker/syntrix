package config

import (
	"testing"
	"time"

	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// Verify backends
	assert.Contains(t, cfg.Backends, "default_mongo")
	assert.Equal(t, "mongo", cfg.Backends["default_mongo"].Type)
	assert.Equal(t, "mongodb://localhost:27017", cfg.Backends["default_mongo"].Mongo.URI)
	assert.Equal(t, "syntrix", cfg.Backends["default_mongo"].Mongo.DatabaseName)

	assert.Contains(t, cfg.Backends, "default_postgres")
	assert.Equal(t, "postgres", cfg.Backends["default_postgres"].Type)
	assert.Equal(t, "postgres://syntrix:syntrix@localhost:5432/syntrix?sslmode=disable", cfg.Backends["default_postgres"].Postgres.DSN)

	// Verify topology - Document
	assert.Equal(t, "single", cfg.Topology.Document.Strategy)
	assert.Equal(t, "default_mongo", cfg.Topology.Document.Primary)
	assert.Equal(t, "documents", cfg.Topology.Document.DataCollection)
	assert.Equal(t, "sys", cfg.Topology.Document.SysCollection)
	assert.Equal(t, 5*time.Minute, cfg.Topology.Document.SoftDeleteRetention)

	// Verify topology - User (now uses postgres)
	assert.Equal(t, "single", cfg.Topology.User.Strategy)
	assert.Equal(t, "default_postgres", cfg.Topology.User.Primary)
	assert.Equal(t, "auth_users", cfg.Topology.User.Collection)

	// Verify topology - Revocation
	assert.Equal(t, "single", cfg.Topology.Revocation.Strategy)
	assert.Equal(t, "default_mongo", cfg.Topology.Revocation.Primary)
	assert.Equal(t, "revocations", cfg.Topology.Revocation.Collection)

	// Verify databases
	assert.Contains(t, cfg.Databases, "default")
	assert.Equal(t, "default_mongo", cfg.Databases["default"].Backend)
}

func TestConfig_Validate(t *testing.T) {
	cfg := DefaultConfig()

	err := cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}

func TestConfig_Validate_EmptyConfig(t *testing.T) {
	cfg := Config{}

	err := cfg.Validate(services.ModeDistributed)
	// Empty config fails because databases.default is required
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage.databases.default is required")
}

func TestConfig_ApplyDefaults(t *testing.T) {
	tests := []struct {
		name    string
		initial Config
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name:    "empty config gets all defaults",
			initial: Config{},
			check: func(t *testing.T, cfg *Config) {
				assert.NotNil(t, cfg.Backends)
				assert.Contains(t, cfg.Backends, "default_mongo")
				assert.Contains(t, cfg.Backends, "default_postgres")
				assert.NotNil(t, cfg.Databases)
				assert.Contains(t, cfg.Databases, "default")
				assert.Equal(t, "single", cfg.Topology.Document.Strategy)
				assert.Equal(t, "default_mongo", cfg.Topology.Document.Primary)
				assert.Equal(t, "documents", cfg.Topology.Document.DataCollection)
				assert.Equal(t, "sys", cfg.Topology.Document.SysCollection)
				assert.Equal(t, 5*time.Minute, cfg.Topology.Document.SoftDeleteRetention)
				assert.Equal(t, "single", cfg.Topology.User.Strategy)
				assert.Equal(t, "default_postgres", cfg.Topology.User.Primary)
				assert.Equal(t, "auth_users", cfg.Topology.User.Collection)
				assert.Equal(t, "single", cfg.Topology.Revocation.Strategy)
				assert.Equal(t, "default_mongo", cfg.Topology.Revocation.Primary)
				assert.Equal(t, "revocations", cfg.Topology.Revocation.Collection)
			},
		},
		{
			name: "custom backends preserved",
			initial: Config{
				Backends: map[string]BackendConfig{
					"custom_backend": {
						Type: "mongo",
						Mongo: MongoConfig{
							URI:          "mongodb://custom:27017",
							DatabaseName: "custom_db",
						},
					},
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Contains(t, cfg.Backends, "custom_backend")
				assert.Equal(t, "mongodb://custom:27017", cfg.Backends["custom_backend"].Mongo.URI)
			},
		},
		{
			name: "custom topology preserved",
			initial: Config{
				Topology: TopologyConfig{
					Document: DocumentTopology{
						BaseTopology: BaseTopology{
							Strategy: "read_write_split",
							Primary:  "primary_backend",
							Replica:  "replica_backend",
						},
						DataCollection:      "custom_docs",
						SysCollection:       "custom_sys",
						SoftDeleteRetention: 10 * time.Minute,
					},
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "read_write_split", cfg.Topology.Document.Strategy)
				assert.Equal(t, "primary_backend", cfg.Topology.Document.Primary)
				assert.Equal(t, "replica_backend", cfg.Topology.Document.Replica)
				assert.Equal(t, "custom_docs", cfg.Topology.Document.DataCollection)
				assert.Equal(t, "custom_sys", cfg.Topology.Document.SysCollection)
				assert.Equal(t, 10*time.Minute, cfg.Topology.Document.SoftDeleteRetention)
			},
		},
		{
			name: "partial config gets remaining defaults",
			initial: Config{
				Topology: TopologyConfig{
					Document: DocumentTopology{
						BaseTopology: BaseTopology{
							Strategy: "custom_strategy",
							// Primary is empty, should get default
						},
						// DataCollection is empty, should get default
					},
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "custom_strategy", cfg.Topology.Document.Strategy)
				assert.Equal(t, "default_mongo", cfg.Topology.Document.Primary)
				assert.Equal(t, "documents", cfg.Topology.Document.DataCollection)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.initial
			cfg.ApplyDefaults()
			tt.check(t, &cfg)
		})
	}
}

func TestConfig_ApplyEnvOverrides(t *testing.T) {
	tests := []struct {
		name    string
		envVars map[string]string
		initial Config
		check   func(t *testing.T, cfg *Config)
	}{
		{
			name: "MONGO_URI overrides default backend URI",
			envVars: map[string]string{
				"MONGO_URI": "mongodb://envhost:27017",
			},
			initial: Config{
				Backends: map[string]BackendConfig{
					"default_mongo": {
						Type: "mongo",
						Mongo: MongoConfig{
							URI:          "mongodb://localhost:27017",
							DatabaseName: "syntrix",
						},
					},
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "mongodb://envhost:27017", cfg.Backends["default_mongo"].Mongo.URI)
			},
		},
		{
			name: "DB_NAME overrides default backend database name",
			envVars: map[string]string{
				"DB_NAME": "custom_database",
			},
			initial: Config{
				Backends: map[string]BackendConfig{
					"default_mongo": {
						Type: "mongo",
						Mongo: MongoConfig{
							URI:          "mongodb://localhost:27017",
							DatabaseName: "syntrix",
						},
					},
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "custom_database", cfg.Backends["default_mongo"].Mongo.DatabaseName)
			},
		},
		{
			name: "both env vars override",
			envVars: map[string]string{
				"MONGO_URI": "mongodb://prod:27017",
				"DB_NAME":   "production",
			},
			initial: Config{
				Backends: map[string]BackendConfig{
					"default_mongo": {
						Type: "mongo",
						Mongo: MongoConfig{
							URI:          "mongodb://localhost:27017",
							DatabaseName: "syntrix",
						},
					},
				},
			},
			check: func(t *testing.T, cfg *Config) {
				assert.Equal(t, "mongodb://prod:27017", cfg.Backends["default_mongo"].Mongo.URI)
				assert.Equal(t, "production", cfg.Backends["default_mongo"].Mongo.DatabaseName)
			},
		},
		{
			name: "no default_mongo backend, env vars ignored",
			envVars: map[string]string{
				"MONGO_URI": "mongodb://envhost:27017",
			},
			initial: Config{
				Backends: map[string]BackendConfig{
					"other_backend": {
						Type: "mongo",
						Mongo: MongoConfig{
							URI: "mongodb://other:27017",
						},
					},
				},
			},
			check: func(t *testing.T, cfg *Config) {
				// other_backend should not be modified
				assert.Equal(t, "mongodb://other:27017", cfg.Backends["other_backend"].Mongo.URI)
				// default_mongo should not exist
				_, exists := cfg.Backends["default_mongo"]
				assert.False(t, exists)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			cfg := tt.initial
			cfg.ApplyEnvOverrides()
			tt.check(t, &cfg)
		})
	}
}

func TestConfig_ResolvePaths(t *testing.T) {
	// ResolvePaths is a no-op for storage config
	cfg := DefaultConfig()
	originalURI := cfg.Backends["default_mongo"].Mongo.URI

	cfg.ResolvePaths("/some/base/dir", "data")

	assert.Equal(t, originalURI, cfg.Backends["default_mongo"].Mongo.URI)
}
