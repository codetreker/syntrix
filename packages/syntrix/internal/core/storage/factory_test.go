package storage

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func expectSchemaInitialization(mock sqlmock.Sqlmock, table string) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS " + table).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
}

func factoryTestConfig(strategy string) config.Config {
	return config.Config{
		Topology: config.TopologyConfig{
			Document: config.DocumentTopology{
				BaseTopology:   config.BaseTopology{Strategy: strategy, Primary: "primary", Replica: "replica"},
				DataCollection: "documents", SysCollection: "sys", SoftDeleteRetention: time.Minute,
			},
		},
		Databases: map[string]config.DatabaseConfig{
			"default":  {Backend: "dedicated"},
			"accounts": {Backend: "dedicated"},
		},
	}
}

func TestNewFactoryPreservesPlacementAndRawNamespaces(t *testing.T) {
	for _, strategy := range []string{"single", "read_write_split"} {
		mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
		mt.Run(strategy, func(mt *mtest.T) {
			t := mt.T
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			expectSchemaInitialization(mock, "databases")
			mock.ExpectClose()
			providers := map[string]Provider{
				"primary":   &backendTestProvider{client: mt.Client, dbName: "primary_data"},
				"replica":   &backendTestProvider{client: mt.Client, dbName: "replica_data"},
				"dedicated": &backendTestProvider{client: mt.Client, dbName: "dedicated_data"},
			}
			backends := &Backends{providers: providers, postgresDB: db}
			factory, err := NewFactory(ctx, factoryTestConfig(strategy), backends)
			require.NoError(t, err)
			require.NotNil(t, factory.Document())
			require.NotNil(t, factory.Database())

			primaryRead := "primary_data"
			if strategy == "read_write_split" {
				primaryRead = "replica_data"
			}
			cases := []struct {
				namespace     string
				path          string
				authoritative bool
				physical      string
				collection    string
			}{
				{"default", "orders/1", false, primaryRead, "documents"},
				{"unlisted", "orders/1", false, primaryRead, "documents"},
				{"id:0123456789abcdef", "orders/1", true, "primary_data", "documents"},
				{"accounts", "orders/1", false, "dedicated_data", "documents"},
				{"accounts", "sys/rules", true, "dedicated_data", "sys"},
			}
			for _, test := range cases {
				key := types.CalculateDatabase(test.namespace, test.path)
				mt.AddMockResponses(mtest.CreateCursorResponse(0, test.physical+"."+test.collection, mtest.FirstBatch,
					bson.D{{Key: "_id", Value: key}, {Key: "database", Value: test.namespace}, {Key: "fullpath", Value: test.path}, {Key: "data", Value: bson.D{{Key: "value", Value: 1}}}},
				))
				options := types.ReadOptions{}
				if test.authoritative {
					options.Consistency = types.ReadAuthoritative
				}
				document, err := factory.Document().Get(ctx, test.namespace, test.path, options)
				require.NoError(t, err)
				assert.Equal(t, test.namespace, document.Database)
				command := mt.GetStartedEvent()
				require.NotNil(t, command)
				assert.Equal(t, test.physical, command.DatabaseName)
				assert.Equal(t, test.collection, command.Command.Lookup("find").StringValue())
				filter := command.Command.Lookup("filter").Document()
				assert.Equal(t, test.namespace, filter.Lookup("database").StringValue())
				assert.Equal(t, key, filter.Lookup("_id").StringValue())
			}

			for _, provider := range providers {
				assert.Zero(t, provider.(*backendTestProvider).closes)
			}
			require.NoError(t, backends.Close())
			for _, provider := range providers {
				assert.Equal(t, 1, provider.(*backendTestProvider).closes)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDocumentRouterUsesPrimaryForWritesAndWatch(t *testing.T) {
	client, err := mongo.NewClient()
	require.NoError(t, err)
	backends := &Backends{providers: map[string]Provider{
		"primary": &backendTestProvider{client: client, dbName: "primary_data"},
		"replica": &backendTestProvider{client: client, dbName: "replica_data"},
	}}
	router, err := createDocumentRouter(factoryTestConfig("read_write_split").Topology.Document, backends)
	require.NoError(t, err)
	read, err := router.Select("default", types.OpRead)
	require.NoError(t, err)
	write, err := router.Select("default", types.OpWrite)
	require.NoError(t, err)
	watch, err := router.Select("default", types.OpWatch)
	require.NoError(t, err)
	assert.NotSame(t, read, write)
	assert.Same(t, write, watch)
}

func TestNewFactoryRoutingFailureDoesNotCloseBorrowedConnections(t *testing.T) {
	tests := []struct {
		name     string
		change   func(*config.Config)
		expected string
	}{
		{"primary missing", func(cfg *config.Config) { cfg.Topology.Document.Primary = "missing" }, "backend not found: missing"},
		{"primary wrong provider", func(cfg *config.Config) { cfg.Topology.Document.Primary = "generic" }, "not a mongo provider"},
		{"unsupported strategy", func(cfg *config.Config) { cfg.Topology.Document.Strategy = "unknown" }, "unsupported strategy: unknown"},
		{"replica missing", func(cfg *config.Config) { cfg.Topology.Document.Replica = "missing" }, "backend not found: missing"},
		{"dedicated missing", func(cfg *config.Config) { cfg.Databases["accounts"] = config.DatabaseConfig{Backend: "missing"} }, "backend not found: missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := mongo.NewClient()
			require.NoError(t, err)
			provider := &backendTestProvider{client: client, dbName: "data"}
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			mock.ExpectClose()
			backends := &Backends{providers: map[string]Provider{"primary": provider, "replica": &backendTestProvider{client: client, dbName: "replica_data"}, "dedicated": &backendTestProvider{client: client, dbName: "dedicated_data"}, "generic": &backendGenericProvider{}}, postgresDB: db}
			cfg := factoryTestConfig("read_write_split")
			test.change(&cfg)
			factory, err := NewFactory(context.Background(), cfg, backends)
			require.Nil(t, factory)
			require.ErrorContains(t, err, test.expected)
			assert.Zero(t, provider.closes)
			require.NoError(t, db.Ping())
			require.NoError(t, backends.Close())
			assert.Equal(t, 1, provider.closes)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

type factoryCancelOnLock struct{ cancel context.CancelFunc }

func (c factoryCancelOnLock) Match(driver.Value) bool { c.cancel(); return true }

func TestNewFactoryCatalogFailureDoesNotCloseBorrowedConnections(t *testing.T) {
	for _, phase := range []string{"schema", "cancellation"} {
		t.Run(phase, func(t *testing.T) {
			client, err := mongo.NewClient()
			require.NoError(t, err)
			provider := &backendTestProvider{client: client, dbName: "data"}
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("schema failed")
			mock.ExpectBegin()
			if phase == "schema" {
				mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("CREATE TABLE IF NOT EXISTS databases").WillReturnError(failure)
			} else {
				mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(factoryCancelOnLock{cancel}).WillDelayFor(time.Second).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectRollback()
			mock.ExpectClose()
			backends := &Backends{providers: map[string]Provider{"primary": provider}, postgresDB: db}
			cfg := factoryTestConfig("single")
			cfg.Databases = nil
			factory, err := NewFactory(ctx, cfg, backends)
			require.Nil(t, factory)
			if phase == "schema" {
				assert.ErrorIs(t, err, failure)
			} else {
				assert.ErrorIs(t, err, context.Canceled)
			}
			assert.Zero(t, provider.closes)
			require.NoError(t, db.Ping())
			require.NoError(t, backends.Close())
			assert.Equal(t, 1, provider.closes)
			require.EventuallyWithT(t, func(c *assert.CollectT) { require.NoError(c, mock.ExpectationsWereMet()) }, time.Second, time.Millisecond)
		})
	}
}

func TestFactoryCatalogWorksWithoutUserInitialization(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	expectSchemaInitialization(mock, "databases")
	mock.ExpectQuery("SELECT id, slug, display_name").WithArgs("0123456789abcdef").WillReturnRows(sqlmock.NewRows([]string{
		"id", "slug", "display_name", "description", "owner_id", "created_at", "updated_at", "max_documents", "max_storage_bytes", "status",
	}).AddRow("0123456789abcdef", "app", "App", nil, "existing-owner", time.Now(), time.Now(), 0, 0, "active"))
	mock.ExpectClose()
	client, err := mongo.NewClient()
	require.NoError(t, err)
	backends := &Backends{providers: map[string]Provider{"primary": &backendTestProvider{client: client, dbName: "data"}}, postgresDB: db}
	cfg := factoryTestConfig("single")
	cfg.Databases = nil
	factory, err := NewFactory(context.Background(), cfg, backends)
	require.NoError(t, err)
	database, err := factory.Database().Get(context.Background(), "0123456789abcdef")
	require.NoError(t, err)
	assert.Equal(t, "existing-owner", database.OwnerID)
	require.NoError(t, backends.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}
