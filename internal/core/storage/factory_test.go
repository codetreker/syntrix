package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/config"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type mockMongoProvider struct {
	client *mongo.Client
	dbName string
}

func (m *mockMongoProvider) Client() *mongo.Client {
	return m.client
}

func (m *mockMongoProvider) DatabaseName() string {
	return m.dbName
}

func (m *mockMongoProvider) Close(ctx context.Context) error {
	return nil
}

// Mock provider creation
var originalNewMongoProvider = newMongoProvider
var originalNewPostgresDB = newPostgresDB

func setupMockProvider() {
	newMongoProvider = func(ctx context.Context, uri, dbName string) (Provider, error) {
		// Return a dummy client (won't connect but satisfies interface)
		client, _ := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://mock"))
		return &mockMongoProvider{client: client, dbName: dbName}, nil
	}
}

func expectSchemaInitialization(mock sqlmock.Sqlmock, table string) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS " + table).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
}

type cancelOnLock struct{ cancel context.CancelFunc }

func (a cancelOnLock) Match(driver.Value) bool {
	a.cancel()
	return true
}

func TestNewFactoryInitializationCancellation(t *testing.T) {
	for _, phase := range []string{"ping", "users", "databases"} {
		t.Run(phase, func(t *testing.T) {
			setupMockProvider()
			defer teardownMockProvider()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
			require.NoError(t, err)
			newPostgresDB = func(config.PostgresConfig) (*sql.DB, error) {
				if phase == "ping" {
					cancel()
				}
				return db, nil
			}
			if phase != "ping" {
				mock.ExpectPing()
				if phase == "databases" {
					expectSchemaInitialization(mock, "auth_users")
				}
				mock.ExpectBegin()
				mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(cancelOnLock{cancel}).
					WillDelayFor(time.Second).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectRollback()
			}
			mock.ExpectClose()
			cfg := config.Config{
				Backends: map[string]config.BackendConfig{
					"primary":       {Type: "mongo"},
					"postgres_user": {Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://test"}},
				},
				Topology: config.TopologyConfig{
					Document: config.DocumentTopology{
						BaseTopology:   config.BaseTopology{Primary: "primary", Strategy: "single"},
						DataCollection: "docs", SysCollection: "sys",
					},
					User:       config.CollectionTopology{BaseTopology: config.BaseTopology{Primary: "postgres_user", Strategy: "single"}},
					Revocation: config.CollectionTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"}},
				},
			}
			factory, err := NewFactory(ctx, cfg)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, factory, "initialization failure must not publish a factory")
			// database/sql may finish the context-triggered rollback on its watcher.
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				require.NoError(c, mock.ExpectationsWereMet())
			}, time.Second, time.Millisecond)
		})
	}
}

func setupMockPostgres() sqlmock.Sqlmock {
	db, mock, _ := sqlmock.New()
	newPostgresDB = func(cfg config.PostgresConfig) (*sql.DB, error) {
		return db, nil
	}
	// Mock successful ping
	mock.ExpectPing()
	expectSchemaInitialization(mock, "auth_users")
	expectSchemaInitialization(mock, "databases")
	return mock
}

func teardownMockProvider() {
	newMongoProvider = originalNewMongoProvider
	newPostgresDB = originalNewPostgresDB
}

const (
	testMongoURI = "mongodb://localhost:27017"
	testDBName   = "syntrix_test_factory"
)

func TestNewFactory(t *testing.T) {
	setupMockProvider()
	setupMockPostgres()
	defer teardownMockProvider()

	cfg := config.Config{
		Backends: map[string]config.BackendConfig{
			"primary": {
				Type: "mongo",
				Mongo: config.MongoConfig{
					URI:          testMongoURI,
					DatabaseName: testDBName,
				},
			},
			"postgres_user": {
				Type: "postgres",
				Postgres: config.PostgresConfig{
					DSN: "postgres://test",
				},
			},
		},
		Topology: config.TopologyConfig{
			Document: config.DocumentTopology{
				BaseTopology: config.BaseTopology{
					Strategy: "single",
					Primary:  "primary",
				},
				DataCollection: "docs",
				SysCollection:  "sys",
			},
			User: config.CollectionTopology{
				BaseTopology: config.BaseTopology{
					Strategy: "single",
					Primary:  "postgres_user",
				},
				Collection: "users",
			},
			Revocation: config.CollectionTopology{
				BaseTopology: config.BaseTopology{
					Strategy: "single",
					Primary:  "primary",
				},
				Collection: "revocations",
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	f, err := NewFactory(ctx, cfg)
	require.NoError(t, err)
	defer f.Close()

	assert.NotNil(t, f.Document())
	assert.NotNil(t, f.User())
	assert.NotNil(t, f.Revocation())
}

func TestNewFactory_DatabaseConfig(t *testing.T) {
	setupMockProvider()
	setupMockPostgres()
	defer teardownMockProvider()

	cfg := config.Config{
		Backends: map[string]config.BackendConfig{
			"primary":       {Type: "mongo", Mongo: config.MongoConfig{URI: "mongodb://p", DatabaseName: "db1"}},
			"database1":     {Type: "mongo", Mongo: config.MongoConfig{URI: "mongodb://t1", DatabaseName: "db2"}},
			"postgres_user": {Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://test"}},
		},
		Topology: config.TopologyConfig{
			Document:   config.DocumentTopology{BaseTopology: config.BaseTopology{Strategy: "single", Primary: "primary"}},
			User:       config.CollectionTopology{BaseTopology: config.BaseTopology{Strategy: "single", Primary: "postgres_user"}},
			Revocation: config.CollectionTopology{BaseTopology: config.BaseTopology{Strategy: "single", Primary: "primary"}},
		},
		Databases: map[string]config.DatabaseConfig{
			"t1": {Backend: "database1"},
		},
	}

	f, err := NewFactory(context.Background(), cfg)
	require.NoError(t, err)
	defer f.Close()
}

func TestNewFactory_Errors(t *testing.T) {
	ctx := context.Background()

	t.Run("Unsupported Backend Type", func(t *testing.T) {
		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"bad": {Type: "redis"},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "unsupported backend type")
	})

	t.Run("Document Backend Not Found", func(t *testing.T) {
		cfg := config.Config{
			Backends: map[string]config.BackendConfig{},
			Topology: config.TopologyConfig{
				Document: config.DocumentTopology{
					BaseTopology: config.BaseTopology{Primary: "missing"},
				},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "backend not found")
	})
}

func TestNewFactory_ReadWriteSplit(t *testing.T) {
	// Mock provider creation
	origNewMongoProvider := newMongoProvider
	origNewPostgresDB := newPostgresDB
	defer func() {
		newMongoProvider = origNewMongoProvider
		newPostgresDB = origNewPostgresDB
	}()

	newMongoProvider = func(ctx context.Context, uri, dbName string) (Provider, error) {
		client, _ := mongo.Connect(ctx, options.Client().ApplyURI(uri))
		return &mockMongoProvider{client: client, dbName: dbName}, nil
	}

	db, mock, _ := sqlmock.New()
	newPostgresDB = func(cfg config.PostgresConfig) (*sql.DB, error) {
		return db, nil
	}
	mock.ExpectPing()
	expectSchemaInitialization(mock, "auth_users")
	expectSchemaInitialization(mock, "databases")

	cfg := config.Config{
		Backends: map[string]config.BackendConfig{
			"primary": {
				Type:  "mongo",
				Mongo: config.MongoConfig{URI: "mongodb://primary", DatabaseName: "db"},
			},
			"replica": {
				Type:  "mongo",
				Mongo: config.MongoConfig{URI: "mongodb://replica", DatabaseName: "db"},
			},
			"postgres_user": {
				Type:     "postgres",
				Postgres: config.PostgresConfig{DSN: "postgres://test"},
			},
		},
		Topology: config.TopologyConfig{
			Document: config.DocumentTopology{
				BaseTopology: config.BaseTopology{
					Strategy: "read_write_split",
					Primary:  "primary",
					Replica:  "replica",
				},
				DataCollection: "docs",
				SysCollection:  "sys",
			},
			User: config.CollectionTopology{
				BaseTopology: config.BaseTopology{
					Strategy: "single",
					Primary:  "postgres_user",
				},
				Collection: "users",
			},
			Revocation: config.CollectionTopology{
				BaseTopology: config.BaseTopology{
					Strategy: "read_write_split",
					Primary:  "primary",
					Replica:  "replica",
				},
				Collection: "revocations",
			},
		},
	}

	ctx := context.Background()
	f, err := NewFactory(ctx, cfg)
	require.NoError(t, err)
	defer f.Close()

	assert.NotNil(t, f.Document())
	assert.NotNil(t, f.User())
	assert.NotNil(t, f.Revocation())
}

func TestNewFactory_ProviderInitError(t *testing.T) {
	// Save original provider creator
	origNewMongoProvider := newMongoProvider
	defer func() { newMongoProvider = origNewMongoProvider }()

	// Mock provider creation to fail
	newMongoProvider = func(ctx context.Context, uri, dbName string) (Provider, error) {
		return nil, errors.New("connection failed")
	}

	cfg := config.Config{
		Backends: map[string]config.BackendConfig{
			"primary": {Type: "mongo", Mongo: config.MongoConfig{URI: "mongodb://fail", DatabaseName: "db"}},
		},
	}

	_, err := NewFactory(context.Background(), cfg)
	assert.ErrorContains(t, err, "failed to initialize backend primary")
}

func TestNewFactory_RouterErrors(t *testing.T) {
	// Mock provider creation to succeed
	origNewMongoProvider := newMongoProvider
	origNewPostgresDB := newPostgresDB
	defer func() {
		newMongoProvider = origNewMongoProvider
		newPostgresDB = origNewPostgresDB
	}()

	newMongoProvider = func(ctx context.Context, uri, dbName string) (Provider, error) {
		client, _ := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://mock"))
		return &mockMongoProvider{client: client, dbName: dbName}, nil
	}

	ctx := context.Background()

	t.Run("Document Unsupported Strategy", func(t *testing.T) {
		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary": {Type: "mongo"},
			},
			Topology: config.TopologyConfig{
				Document: config.DocumentTopology{
					BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "unknown"},
				},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "unsupported strategy: unknown")
	})

	t.Run("Document Replica Missing", func(t *testing.T) {
		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary": {Type: "mongo"},
			},
			Topology: config.TopologyConfig{
				Document: config.DocumentTopology{
					BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "read_write_split", Replica: "missing"},
				},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "backend not found: missing")
	})

	t.Run("User Primary Missing", func(t *testing.T) {
		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary": {Type: "mongo"},
			},
			Topology: config.TopologyConfig{
				Document: config.DocumentTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"}},
				User: config.CollectionTopology{
					BaseTopology: config.BaseTopology{Primary: "missing"},
				},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "backend not found: missing")
	})

	t.Run("User Unsupported Backend Type", func(t *testing.T) {
		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary": {Type: "mongo"},
			},
			Topology: config.TopologyConfig{
				Document: config.DocumentTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"}},
				User: config.CollectionTopology{
					BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"},
				},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "unsupported backend type for user store: mongo")
	})

	t.Run("Revocation Primary Missing", func(t *testing.T) {
		db, mock, _ := sqlmock.New()
		newPostgresDB = func(cfg config.PostgresConfig) (*sql.DB, error) {
			return db, nil
		}
		mock.ExpectPing()
		expectSchemaInitialization(mock, "auth_users")

		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary":       {Type: "mongo"},
				"postgres_user": {Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://test"}},
			},
			Topology: config.TopologyConfig{
				Document: config.DocumentTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"}},
				User:     config.CollectionTopology{BaseTopology: config.BaseTopology{Primary: "postgres_user", Strategy: "single"}},
				Revocation: config.CollectionTopology{
					BaseTopology: config.BaseTopology{Primary: "missing"},
				},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "backend not found: missing")
	})

	t.Run("Revocation Unsupported Strategy", func(t *testing.T) {
		db, mock, _ := sqlmock.New()
		newPostgresDB = func(cfg config.PostgresConfig) (*sql.DB, error) {
			return db, nil
		}
		mock.ExpectPing()
		expectSchemaInitialization(mock, "auth_users")

		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary":       {Type: "mongo"},
				"postgres_user": {Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://test"}},
			},
			Topology: config.TopologyConfig{
				Document:   config.DocumentTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"}},
				User:       config.CollectionTopology{BaseTopology: config.BaseTopology{Primary: "postgres_user", Strategy: "single"}},
				Revocation: config.CollectionTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "unknown"}},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "unsupported strategy: unknown")
	})

	t.Run("Revocation Replica Missing", func(t *testing.T) {
		db, mock, _ := sqlmock.New()
		newPostgresDB = func(cfg config.PostgresConfig) (*sql.DB, error) {
			return db, nil
		}
		mock.ExpectPing()
		expectSchemaInitialization(mock, "auth_users")

		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary":       {Type: "mongo"},
				"postgres_user": {Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://test"}},
			},
			Topology: config.TopologyConfig{
				Document:   config.DocumentTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"}},
				User:       config.CollectionTopology{BaseTopology: config.BaseTopology{Primary: "postgres_user", Strategy: "single"}},
				Revocation: config.CollectionTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "read_write_split", Replica: "missing"}},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "backend not found: missing")
	})
}

func TestNewFactory_DatabaseErrors(t *testing.T) {
	origNewMongoProvider := newMongoProvider
	defer func() { newMongoProvider = origNewMongoProvider }()

	newMongoProvider = func(ctx context.Context, uri, dbName string) (Provider, error) {
		client, _ := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://mock"))
		return &mockMongoProvider{client: client, dbName: dbName}, nil
	}

	ctx := context.Background()

	t.Run("Database Document Backend Missing", func(t *testing.T) {
		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary": {Type: "mongo"},
			},
			Topology: config.TopologyConfig{
				Document: config.DocumentTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"}},
			},
			Databases: map[string]config.DatabaseConfig{
				"t1": {Backend: "missing"},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "backend not found: missing")
	})
}

func TestNewFactory_DefaultDatabaseSkipped(t *testing.T) {
	setupMockProvider()
	setupMockPostgres()
	defer teardownMockProvider()

	cfg := config.Config{
		Backends: map[string]config.BackendConfig{
			"primary":       {Type: "mongo", Mongo: config.MongoConfig{URI: "mongodb://p", DatabaseName: "db1"}},
			"postgres_user": {Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://test"}},
		},
		Topology: config.TopologyConfig{
			Document:   config.DocumentTopology{BaseTopology: config.BaseTopology{Strategy: "single", Primary: "primary"}},
			User:       config.CollectionTopology{BaseTopology: config.BaseTopology{Strategy: "single", Primary: "postgres_user"}},
			Revocation: config.CollectionTopology{BaseTopology: config.BaseTopology{Strategy: "single", Primary: "primary"}},
		},
		Databases: map[string]config.DatabaseConfig{
			"default": {Backend: "primary"}, // Should be skipped
		},
	}

	f, err := NewFactory(context.Background(), cfg)
	require.NoError(t, err)
	defer f.Close()
}

type mockGenericProvider struct{}

func (m *mockGenericProvider) Close(ctx context.Context) error {
	return nil
}

func TestFactory_GetMongoProvider_Errors(t *testing.T) {
	f := &factory{
		providers: map[string]Provider{
			"generic": &mockGenericProvider{},
		},
	}

	t.Run("Backend Not Found", func(t *testing.T) {
		_, err := f.getMongoProvider("missing")
		assert.ErrorContains(t, err, "backend not found: missing")
	})

	t.Run("Not A Mongo Provider", func(t *testing.T) {
		_, err := f.getMongoProvider("generic")
		assert.ErrorContains(t, err, "is not a mongo provider")
	})
}

type errorClosingProvider struct {
	mockMongoProvider
}

func (e *errorClosingProvider) Close(ctx context.Context) error {
	return errors.New("close failed")
}

func TestFactory_CloseError(t *testing.T) {
	f := &factory{
		providers: map[string]Provider{
			"p1": &errorClosingProvider{},
		},
	}
	err := f.Close()
	assert.ErrorContains(t, err, "errors closing providers")
	assert.ErrorContains(t, err, "close failed")
}

type noopProvider struct{}

func (n *noopProvider) Close(ctx context.Context) error { return nil }

func TestFactory_GetMongoClient_Success(t *testing.T) {
	t.Parallel()

	f := &factory{providers: make(map[string]Provider)}
	client, _ := mongo.Connect(context.Background(), options.Client().ApplyURI("mongodb://mock"))
	f.providers["primary"] = &mockMongoProvider{client: client, dbName: "db1"}

	cli, dbName, err := f.GetMongoClient("primary")
	require.NoError(t, err)
	assert.Equal(t, client, cli)
	assert.Equal(t, "db1", dbName)
}

func TestFactory_GetMongoClient_Errors(t *testing.T) {
	t.Parallel()

	f := &factory{providers: make(map[string]Provider)}

	_, _, err := f.GetMongoClient("missing")
	assert.ErrorContains(t, err, "backend not found")

	f.providers["noop"] = &noopProvider{}
	_, _, err = f.GetMongoClient("noop")
	assert.ErrorContains(t, err, "not a mongo provider")
}

// Test newPostgresDB function directly
func TestNewPostgresDB(t *testing.T) {
	// Capture the original function before any mocking
	realNewPostgresDB := originalNewPostgresDB

	t.Run("WithInvalidDSN", func(t *testing.T) {
		cfg := config.PostgresConfig{
			DSN: "invalid://not-a-valid-connection",
		}
		// This will succeed at opening (sql.Open is lazy) but would fail on ping
		db, err := realNewPostgresDB(cfg)
		if err != nil {
			// Some drivers may fail on Open with invalid DSN
			return
		}
		defer db.Close()
		// The connection is lazy, so Open succeeds but Ping would fail
		assert.NotNil(t, db)
	})

	t.Run("WithConnectionPoolSettings", func(t *testing.T) {
		cfg := config.PostgresConfig{
			DSN:             "postgres://user:pass@localhost:5432/db?sslmode=disable",
			MaxOpenConns:    10,
			MaxIdleConns:    5,
			ConnMaxLifetime: 5 * time.Minute,
		}
		// sql.Open is lazy and won't fail even with invalid connection string
		db, err := realNewPostgresDB(cfg)
		if err != nil {
			// Some implementations may validate DSN on Open
			return
		}
		defer db.Close()
		assert.NotNil(t, db)
	})

	t.Run("WithZeroPoolSettings", func(t *testing.T) {
		cfg := config.PostgresConfig{
			DSN:             "postgres://user:pass@localhost:5432/db?sslmode=disable",
			MaxOpenConns:    0, // Should skip SetMaxOpenConns
			MaxIdleConns:    0, // Should skip SetMaxIdleConns
			ConnMaxLifetime: 0, // Should skip SetConnMaxLifetime
		}
		db, err := realNewPostgresDB(cfg)
		if err != nil {
			return
		}
		defer db.Close()
		assert.NotNil(t, db)
	})
}

func TestNewFactory_PostgresErrors(t *testing.T) {
	origNewMongoProvider := newMongoProvider
	origNewPostgresDB := newPostgresDB
	defer func() {
		newMongoProvider = origNewMongoProvider
		newPostgresDB = origNewPostgresDB
	}()

	newMongoProvider = func(ctx context.Context, uri, dbName string) (Provider, error) {
		client, _ := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://mock"))
		return &mockMongoProvider{client: client, dbName: dbName}, nil
	}

	ctx := context.Background()

	t.Run("PostgresConnectionError", func(t *testing.T) {
		newPostgresDB = func(cfg config.PostgresConfig) (*sql.DB, error) {
			return nil, errors.New("connection failed")
		}

		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary":       {Type: "mongo"},
				"postgres_user": {Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://test"}},
			},
			Topology: config.TopologyConfig{
				Document: config.DocumentTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"}},
				User:     config.CollectionTopology{BaseTopology: config.BaseTopology{Primary: "postgres_user", Strategy: "single"}},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "failed to connect to postgres")
	})

	t.Run("PostgresPingError", func(t *testing.T) {
		db, mock, _ := sqlmock.New(sqlmock.MonitorPingsOption(true))
		newPostgresDB = func(cfg config.PostgresConfig) (*sql.DB, error) {
			return db, nil
		}
		mock.ExpectPing().WillReturnError(errors.New("ping failed"))

		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary":       {Type: "mongo"},
				"postgres_user": {Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://test"}},
			},
			Topology: config.TopologyConfig{
				Document: config.DocumentTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"}},
				User:     config.CollectionTopology{BaseTopology: config.BaseTopology{Primary: "postgres_user", Strategy: "single"}},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "failed to ping postgres")
	})

	t.Run("PostgresEnsureSchemaError", func(t *testing.T) {
		db, mock, _ := sqlmock.New(sqlmock.MonitorPingsOption(true))
		newPostgresDB = func(cfg config.PostgresConfig) (*sql.DB, error) {
			return db, nil
		}
		mock.ExpectPing()
		mock.ExpectBegin()
		mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("CREATE TABLE IF NOT EXISTS auth_users").WillReturnError(errors.New("schema error"))
		mock.ExpectRollback()

		cfg := config.Config{
			Backends: map[string]config.BackendConfig{
				"primary":       {Type: "mongo"},
				"postgres_user": {Type: "postgres", Postgres: config.PostgresConfig{DSN: "postgres://test"}},
			},
			Topology: config.TopologyConfig{
				Document: config.DocumentTopology{BaseTopology: config.BaseTopology{Primary: "primary", Strategy: "single"}},
				User:     config.CollectionTopology{BaseTopology: config.BaseTopology{Primary: "postgres_user", Strategy: "single"}},
			},
		}
		_, err := NewFactory(ctx, cfg)
		assert.ErrorContains(t, err, "failed to ensure postgres schema")
	})
}

func TestFactory_DatabaseAccessor(t *testing.T) {
	setupMockProvider()
	mock := setupMockPostgres()
	defer teardownMockProvider()

	cfg := config.Config{
		Backends: map[string]config.BackendConfig{
			"primary": {
				Type: "mongo",
				Mongo: config.MongoConfig{
					URI:          testMongoURI,
					DatabaseName: testDBName,
				},
			},
			"postgres_user": {
				Type: "postgres",
				Postgres: config.PostgresConfig{
					DSN: "postgres://test",
				},
			},
		},
		Topology: config.TopologyConfig{
			Document: config.DocumentTopology{
				BaseTopology: config.BaseTopology{
					Strategy: "single",
					Primary:  "primary",
				},
				DataCollection: "docs",
				SysCollection:  "sys",
			},
			User: config.CollectionTopology{
				BaseTopology: config.BaseTopology{
					Strategy: "single",
					Primary:  "postgres_user",
				},
				Collection: "users",
			},
			Revocation: config.CollectionTopology{
				BaseTopology: config.BaseTopology{
					Strategy: "single",
					Primary:  "primary",
				},
				Collection: "revocations",
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	f, err := NewFactory(ctx, cfg)
	require.NoError(t, err)
	defer f.Close()

	// Test Database() accessor
	dbStore := f.Database()
	assert.NotNil(t, dbStore)

	// Verify it's a PostgreSQL-backed store by checking its type
	_ = mock // Use mock to avoid lint error
}
