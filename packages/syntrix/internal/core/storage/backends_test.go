package storage

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
)

type backendTestProvider struct {
	client          *mongo.Client
	dbName          string
	closes          int
	closeErr        error
	closeContextErr error
}

func (p *backendTestProvider) Client() *mongo.Client { return p.client }
func (p *backendTestProvider) DatabaseName() string  { return p.dbName }
func (p *backendTestProvider) Close(ctx context.Context) error {
	p.closes++
	p.closeContextErr = ctx.Err()
	return p.closeErr
}

func backendTestConfig() config.Config {
	cfg := config.Config{
		Backends: map[string]config.BackendConfig{
			"a_mongo":  {Type: "mongo", Mongo: config.MongoConfig{URI: "mongodb://primary", DatabaseName: "primary_data"}},
			"b_unused": {Type: "mongo", Mongo: config.MongoConfig{URI: "mongodb://unused", DatabaseName: "unused_data"}},
			"postgres": {Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://test", MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Minute}},
		},
	}
	cfg.Topology.User.Primary = "postgres"
	return cfg
}

func mockBackendConstructors(t *testing.T) map[string]*backendTestProvider {
	t.Helper()
	originalMongo, originalPostgres := newMongoProvider, newPostgresDB
	t.Cleanup(func() { newMongoProvider, newPostgresDB = originalMongo, originalPostgres })
	providers := make(map[string]*backendTestProvider)
	newMongoProvider = func(ctx context.Context, uri, dbName string) (Provider, error) {
		client, err := mongo.NewClient()
		if err != nil {
			return nil, err
		}
		provider := &backendTestProvider{client: client, dbName: dbName}
		providers[uri] = provider
		return provider, nil
	}
	return providers
}

func TestNewBackendsSharesConfiguredConnections(t *testing.T) {
	providers := mockBackendConstructors(t)
	cfg := backendTestConfig()
	cfg.Backends["other_postgres"] = config.BackendConfig{Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://unused"}}
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	mock.ExpectPing()
	mock.ExpectClose()
	opens := 0
	newPostgresDB = func(pg config.PostgresConfig) (*sql.DB, error) {
		opens++
		assert.Equal(t, cfg.Backends["postgres"].Postgres, pg)
		return db, nil
	}

	backends, err := NewBackends(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, providers, 2, "all configured Mongo backends remain startup dependencies")
	assert.Equal(t, 1, opens)
	assert.Same(t, db, backends.PostgresDB())
	for name, expected := range map[string]string{"a_mongo": "primary_data", "b_unused": "unused_data"} {
		client, dbName, err := backends.GetMongoClient(name)
		require.NoError(t, err)
		assert.Equal(t, expected, dbName)
		again, _, err := backends.GetMongoClient(name)
		require.NoError(t, err)
		assert.Same(t, client, again)
	}
	require.NoError(t, backends.Close())
	require.NoError(t, backends.Close())
	for _, provider := range providers {
		assert.Equal(t, 1, provider.closes)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNewBackendsConfigurationFailuresCloseAllocatedProviders(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*config.Config)
		message string
	}{
		{"unsupported backend", func(cfg *config.Config) { cfg.Backends["z_invalid"] = config.BackendConfig{Type: "redis"} }, "unsupported backend type: redis"},
		{"unused postgres missing DSN", func(cfg *config.Config) { cfg.Backends["z_invalid"] = config.BackendConfig{Type: "postgres"} }, "postgres backend z_invalid: DSN is required"},
		{"missing selected postgres", func(cfg *config.Config) { cfg.Topology.User.Primary = "missing" }, "backend not found: missing"},
		{"selected backend is Mongo", func(cfg *config.Config) { cfg.Topology.User.Primary = "a_mongo" }, "only postgres is supported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providers := mockBackendConstructors(t)
			newPostgresDB = func(config.PostgresConfig) (*sql.DB, error) {
				t.Fatal("invalid config must not open PostgreSQL")
				return nil, nil
			}
			cfg := backendTestConfig()
			test.change(&cfg)
			backends, err := NewBackends(context.Background(), cfg)
			require.ErrorContains(t, err, test.message)
			assert.Nil(t, backends)
			require.Len(t, providers, 2)
			for _, provider := range providers {
				assert.Equal(t, 1, provider.closes)
			}
		})
	}
}

func TestNewBackendsPreservesInitializationAndCleanupErrors(t *testing.T) {
	providers := mockBackendConstructors(t)
	openErr, closeErr := errors.New("open failed"), errors.New("close failed")
	openMongo := newMongoProvider
	newMongoProvider = func(ctx context.Context, uri, dbName string) (Provider, error) {
		if uri == "mongodb://unused" {
			return nil, openErr
		}
		provider, err := openMongo(ctx, uri, dbName)
		providers[uri].closeErr = closeErr
		return provider, err
	}
	backends, err := NewBackends(context.Background(), backendTestConfig())
	require.Nil(t, backends)
	assert.ErrorIs(t, err, openErr)
	assert.ErrorIs(t, err, closeErr)
	assert.Equal(t, 1, providers["mongodb://primary"].closes)
}

func TestNewBackendsPostgresFailuresCloseEveryConnection(t *testing.T) {
	for _, phase := range []string{"open", "ping", "canceled ping"} {
		t.Run(phase, func(t *testing.T) {
			providers := mockBackendConstructors(t)
			failure := errors.New("postgres failed")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mock sqlmock.Sqlmock
			if phase == "open" {
				newPostgresDB = func(config.PostgresConfig) (*sql.DB, error) { return nil, failure }
			} else {
				db, sqlMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
				require.NoError(t, err)
				mock = sqlMock
				if phase == "ping" {
					mock.ExpectPing().WillReturnError(failure)
				}
				mock.ExpectClose()
				newPostgresDB = func(config.PostgresConfig) (*sql.DB, error) {
					if phase == "canceled ping" {
						cancel()
					}
					return db, nil
				}
			}
			backends, err := NewBackends(ctx, backendTestConfig())
			require.Nil(t, backends)
			if phase == "canceled ping" {
				assert.ErrorIs(t, err, context.Canceled)
			} else {
				assert.ErrorIs(t, err, failure)
			}
			for _, provider := range providers {
				assert.Equal(t, 1, provider.closes)
				assert.NoError(t, provider.closeContextErr, "cleanup must not inherit canceled initialization")
			}
			if mock != nil {
				require.NoError(t, mock.ExpectationsWereMet())
			}
		})
	}
}

func TestBackendsCloseIsConcurrentIdempotentAndRetainsCauses(t *testing.T) {
	mongoErr, postgresErr := errors.New("mongo close failed"), errors.New("postgres close failed")
	provider := &backendTestProvider{closeErr: mongoErr}
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.ExpectClose().WillReturnError(postgresErr)
	backends := &Backends{providers: map[string]Provider{"primary": provider}, postgresDB: db}
	results := make([]error, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = backends.Close() }(i)
	}
	wg.Wait()
	for _, err := range results {
		assert.ErrorIs(t, err, mongoErr)
		assert.ErrorIs(t, err, postgresErr)
		assert.Same(t, results[0], err)
	}
	assert.Equal(t, 1, provider.closes)
	require.NoError(t, mock.ExpectationsWereMet())
}

type backendGenericProvider struct{}

func (*backendGenericProvider) Close(context.Context) error { return nil }

func TestBackendsMongoLookupErrors(t *testing.T) {
	backends := &Backends{providers: map[string]Provider{"generic": &backendGenericProvider{}}}
	for name, message := range map[string]string{"missing": "backend not found: missing", "generic": "backend generic is not a mongo provider"} {
		client, dbName, err := backends.GetMongoClient(name)
		require.ErrorContains(t, err, message)
		assert.Nil(t, client)
		assert.Empty(t, dbName)
	}
	assert.Nil(t, backends.PostgresDB())
}

func TestNewPostgresDBKeepsConfiguredPoolLimit(t *testing.T) {
	tests := []struct {
		name     string
		config   config.PostgresConfig
		expected int
	}{
		{"defaults", config.PostgresConfig{DSN: "postgres://user:pass@localhost:5432/db?sslmode=disable"}, 0},
		{"configured", config.PostgresConfig{DSN: "postgres://user:pass@localhost:5432/db?sslmode=disable", MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Minute}, 10},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := newPostgresDB(test.config)
			require.NoError(t, err)
			require.Equal(t, test.expected, db.Stats().MaxOpenConnections)
			require.NoError(t, db.Close())
		})
	}
}
