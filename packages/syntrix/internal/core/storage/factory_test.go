package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
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

func TestNewFactoryCatalogFailureDoesNotCloseBorrowedConnections(t *testing.T) {
	t.Run("schema", func(t *testing.T) {
		client, err := mongo.NewClient()
		require.NoError(t, err)
		provider := &backendTestProvider{client: client, dbName: "data"}
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		failure := errors.New("schema failed")
		mock.ExpectBegin()
		mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("CREATE TABLE IF NOT EXISTS databases").WillReturnError(failure)
		mock.ExpectRollback()
		mock.ExpectClose()
		backends := &Backends{providers: map[string]Provider{"primary": provider}, postgresDB: db}
		cfg := factoryTestConfig("single")
		cfg.Databases = nil
		factory, err := NewFactory(ctx, cfg, backends)
		require.Nil(t, factory)
		assert.ErrorIs(t, err, failure)
		assert.Zero(t, provider.closes)
		require.NoError(t, db.Ping())
		require.NoError(t, backends.Close())
		assert.Equal(t, 1, provider.closes)
		require.NoError(t, mock.ExpectationsWereMet())
	})
	t.Run("cancellation", testFactoryCatalogCancellationKeepsBorrowedPool)
}

// SessionResetter and Validator let database/sql retain a connection after
// cancellation rollback, matching lib/pq. Sqlmock lacks these capabilities and
// can lose its connection when automatic rollback wins the explicit rollback.
type catalogCancellationConn struct {
	started     chan struct{}
	rolledBack  chan struct{}
	connections atomic.Int32
	rollbacks   atomic.Int32
	closes      atomic.Int32
	closed      atomic.Bool
}

type catalogCancellationDriver struct{ conn *catalogCancellationConn }

func (d *catalogCancellationDriver) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.conn.connections.Add(1)
	return d.conn, nil
}
func (d *catalogCancellationDriver) Driver() driver.Driver { return d }
func (d *catalogCancellationDriver) Open(string) (driver.Conn, error) {
	return d.Connect(context.Background())
}

func (c *catalogCancellationConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (c *catalogCancellationConn) Begin() (driver.Tx, error) {
	return nil, errors.New("schema initialization must use BeginTx")
}
func (c *catalogCancellationConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &catalogCancellationTx{conn: c}, nil
}
func (c *catalogCancellationConn) ExecContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, "SELECT pg_advisory_xact_lock") {
		return nil, errors.New("schema DDL ran after canceled lock acquisition")
	}
	close(c.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (c *catalogCancellationConn) Ping(ctx context.Context) error { return ctx.Err() }
func (c *catalogCancellationConn) ResetSession(ctx context.Context) error {
	return ctx.Err()
}
func (c *catalogCancellationConn) IsValid() bool { return !c.closed.Load() }
func (c *catalogCancellationConn) Close() error {
	c.closes.Add(1)
	c.closed.Store(true)
	return nil
}

type catalogCancellationTx struct{ conn *catalogCancellationConn }

func (*catalogCancellationTx) Commit() error { return errors.New("canceled transaction committed") }
func (tx *catalogCancellationTx) Rollback() error {
	tx.conn.rollbacks.Add(1)
	close(tx.conn.rolledBack)
	return nil
}

func testFactoryCatalogCancellationKeepsBorrowedPool(t *testing.T) {
	t.Helper()
	testCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	conn := &catalogCancellationConn{started: make(chan struct{}), rolledBack: make(chan struct{})}
	db := sql.OpenDB(&catalogCancellationDriver{conn: conn})
	db.SetMaxOpenConns(1)
	client, err := mongo.NewClient()
	require.NoError(t, err)
	provider := &backendTestProvider{client: client, dbName: "data"}
	backends := &Backends{providers: map[string]Provider{"primary": provider}, postgresDB: db}
	t.Cleanup(func() { require.NoError(t, backends.Close()) })
	cfg := factoryTestConfig("single")
	cfg.Databases = nil
	type result struct {
		factory StorageFactory
		err     error
	}
	completed := make(chan result, 1)
	go func() {
		factory, err := NewFactory(ctx, cfg, backends)
		completed <- result{factory: factory, err: err}
	}()
	select {
	case <-conn.started:
	case <-testCtx.Done():
		t.Fatal(testCtx.Err())
	}
	cancel()
	select {
	case got := <-completed:
		require.Nil(t, got.factory)
		require.ErrorIs(t, got.err, context.Canceled)
	case <-testCtx.Done():
		t.Fatal(testCtx.Err())
	}
	select {
	case <-conn.rolledBack:
	case <-testCtx.Done():
		t.Fatal(testCtx.Err())
	}
	require.NoError(t, db.PingContext(testCtx))
	assert.Equal(t, int32(1), conn.connections.Load())
	assert.Equal(t, int32(1), conn.rollbacks.Load())
	assert.Zero(t, conn.closes.Load())
	assert.Zero(t, provider.closes)
	require.NoError(t, backends.Close())
	require.NoError(t, backends.Close())
	assert.Equal(t, int32(1), conn.closes.Load())
	assert.Equal(t, 1, provider.closes)
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
