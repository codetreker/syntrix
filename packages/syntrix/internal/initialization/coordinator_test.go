package initialization

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	databasepg "github.com/codetreker/syntrix/internal/core/database/postgres"
	"github.com/codetreker/syntrix/internal/core/storage"
	storageconfig "github.com/codetreker/syntrix/internal/core/storage/config"
	storagemongo "github.com/codetreker/syntrix/internal/core/storage/mongo"
	identitypg "github.com/codetreker/syntrix/internal/identity/repository/postgres"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func fixture(t *testing.T) (context.Context, storageconfig.Config, *storage.Backends, *Coordinator) {
	return routedFixture(t, nil)
}

func routedFixture(t *testing.T, configure func(*storageconfig.Config)) (context.Context, storageconfig.Config, *storage.Backends, *Coordinator) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cfg := storageconfig.DefaultConfig()
	dsn := cfg.Backends["default_postgres"].Postgres.DSN
	if value := os.Getenv("POSTGRES_DSN"); value != "" {
		dsn = value
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	name := fmt.Sprintf("initialization_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(name))
	require.NoError(t, err)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := admin.ExecContext(clean, "DROP SCHEMA "+pq.QuoteIdentifier(name)+" CASCADE")
		assert.NoError(t, err)
		assert.NoError(t, admin.Close())
	})
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	if parsed.Scheme == "postgres" || parsed.Scheme == "postgresql" {
		q := parsed.Query()
		q.Set("search_path", name)
		q.Set("application_name", name)
		parsed.RawQuery = q.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=" + name + " application_name=" + name
	}
	backend := cfg.Backends["default_postgres"]
	backend.Postgres.DSN = dsn
	cfg.Backends["default_postgres"] = backend
	backend = cfg.Backends["default_mongo"]
	backend.Mongo.DatabaseName = name
	if uri := os.Getenv("MONGO_URI"); uri != "" {
		backend.Mongo.URI = uri
	}
	cfg.Backends["default_mongo"] = backend
	if configure != nil {
		configure(&cfg)
	}
	backends, err := storage.NewBackends(ctx, cfg)
	require.NoError(t, err)
	mongoNames := []string{}
	for name, backend := range cfg.Backends {
		if backend.Type == "mongo" {
			mongoNames = append(mongoNames, name)
		}
	}
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, backend := range mongoNames {
			client, db, err := backends.GetMongoClient(backend)
			if assert.NoError(t, err) {
				assert.NoError(t, client.Database(db).Drop(clean))
			}
		}
		assert.NoError(t, backends.Close())
	})
	c, err := New(ctx, cfg, backends)
	require.NoError(t, err)
	return ctx, cfg, backends, c
}

func TestInitializationAllRoutedAndSplitPlacements(t *testing.T) {
	t.Parallel()
	ctx, cfg, backends, c := routedFixture(t, func(cfg *storageconfig.Config) {
		primary := cfg.Backends["default_mongo"]
		replica := primary
		replica.Mongo.DatabaseName += "_replica"
		cfg.Backends["replica"] = replica
		routed := primary
		routed.Mongo.DatabaseName += "_routed"
		cfg.Backends["routed"] = routed
		cfg.Backends["alias"] = primary
		cfg.Topology.Document.Strategy = "read_write_split"
		cfg.Topology.Document.Replica = "replica"
		cfg.Topology.Revocation.Strategy = "read_write_split"
		cfg.Topology.Revocation.Replica = "replica"
		cfg.Databases["routed"] = storageconfig.DatabaseConfig{Backend: "routed"}
		cfg.Databases["alias"] = storageconfig.DatabaseConfig{Backend: "alias"}
	})
	require.Len(t, c.placements, 9)
	prepared, err := c.PrepareFresh(ctx)
	require.NoError(t, err)
	assert.Equal(t, Prepared, prepared.State)
	finalized, err := c.Finalize(ctx, prepared.Specification)
	require.NoError(t, err)
	assert.Equal(t, Finalized, finalized.State)
	for _, p := range c.placements {
		assert.True(t, strings.HasPrefix(p.Database, cfg.Backends["default_mongo"].Mongo.DatabaseName))
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = c.PrepareFresh(canceled)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = c.Prepare(canceled)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = c.Finalize(canceled, prepared.Specification)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NoError(t, backends.PostgresDB().PingContext(ctx))
}

func TestInitializationPrepareFinalizeAndReadonlyRestart(t *testing.T) {
	t.Parallel()
	ctx, cfg, backends, c := fixture(t)
	initial, err := c.Inspect(ctx)
	require.NoError(t, err)
	require.Equal(t, Preparing, initial.State)
	_, err = c.Finalize(ctx, initial.Specification)
	require.ErrorIs(t, err, ErrNotPrepared)
	prepared, err := c.PrepareFresh(ctx)
	require.NoError(t, err)
	require.Equal(t, Prepared, prepared.State)
	_, err = c.Finalize(ctx, "wrong-specification")
	require.ErrorIs(t, err, ErrSpecification)
	unchanged, err := c.Inspect(ctx)
	require.NoError(t, err)
	assert.Equal(t, prepared, unchanged)
	// A previous bounded finalizer may have committed one index before losing its
	// response. Repeating the same complete specification converges the remainder.
	first := c.placements[0]
	for _, p := range c.placements {
		if len(p.Cleanup) > 0 {
			first = p
			break
		}
	}
	require.NoError(t, storagemongo.EnsureIndexDefinitions(ctx, first.Handle, first.Cleanup))
	finalized, err := c.Finalize(ctx, prepared.Specification)
	require.NoError(t, err)
	require.Equal(t, Finalized, finalized.State)
	repeated, err := c.Finalize(ctx, prepared.Specification)
	require.NoError(t, err)
	assert.Equal(t, finalized, repeated)
	_, err = c.PrepareFresh(ctx)
	require.ErrorIs(t, err, ErrActiveCleanup)
	encoded, err := json.Marshal(finalized)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "postgres://")
	assert.NotContains(t, string(encoded), "mongodb://")
	for _, p := range c.placements {
		if p.Owner == "sys" {
			assert.Empty(t, p.Cleanup)
		}
	}
	client, db, err := backends.GetMongoClient("default_mongo")
	require.NoError(t, err)
	_, err = client.Database(db).Collection(cfg.Topology.Document.DataCollection).Indexes().DropOne(ctx, "sys_expires_at_1")
	require.NoError(t, err)
	restarted, err := New(ctx, cfg, backends)
	require.NoError(t, err)
	status, err := restarted.Inspect(ctx)
	require.NoError(t, err)
	assert.Equal(t, Prepared, status.State)
	again, err := restarted.Inspect(ctx)
	require.NoError(t, err)
	assert.Equal(t, status, again)
	assert.NoError(t, backends.PostgresDB().PingContext(ctx))
}

func TestInitializationRejectsConflictsBeforeMutation(t *testing.T) {
	t.Parallel()
	ctx, cfg, backends, c := fixture(t)
	client, db, err := backends.GetMongoClient("default_mongo")
	require.NoError(t, err)
	_, err = client.Database(db).Collection(cfg.Topology.Document.DataCollection).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "database", Value: 1}, {Key: "collection_hash", Value: 1}}, Options: options.Index().SetName("database_1_collection_hash_1").SetUnique(true)})
	require.NoError(t, err)
	status, err := c.Inspect(ctx)
	require.NoError(t, err)
	assert.Equal(t, Conflict, status.State)
	_, err = c.Prepare(ctx)
	require.ErrorIs(t, err, ErrConflict)
	_, err = c.Finalize(ctx, status.Specification)
	require.ErrorIs(t, err, ErrConflict)
	var table sql.NullString
	require.NoError(t, backends.PostgresDB().QueryRowContext(ctx, "SELECT to_regclass('auth_users')::text").Scan(&table))
	assert.False(t, table.Valid)
	_, err = client.Database(db).Collection(cfg.Topology.Document.DataCollection).Indexes().DropOne(ctx, "database_1_collection_hash_1")
	require.NoError(t, err)
	_, err = c.Prepare(ctx)
	require.NoError(t, err)
	_, err = backends.PostgresDB().ExecContext(ctx, "ALTER TABLE auth_users ALTER COLUMN username DROP NOT NULL")
	require.NoError(t, err)
	status, err = c.Inspect(ctx)
	require.NoError(t, err)
	assert.Equal(t, Conflict, status.State)
	_, err = c.Prepare(ctx)
	require.ErrorIs(t, err, ErrConflict)
}

func TestInitializationSpecificationStableAndComplete(t *testing.T) {
	t.Parallel()
	ctx, cfg, backends, c := fixture(t)
	first, err := c.Inspect(ctx)
	require.NoError(t, err)
	alias := cfg.Backends["default_mongo"]
	cfg.Backends["alias"] = alias
	cfg.Topology.Document.Strategy = "read_write_split"
	cfg.Topology.Document.Replica = "default_mongo"
	cfg.Topology.Revocation.Strategy = "read_write_split"
	cfg.Topology.Revocation.Replica = "default_mongo"
	cfg.Databases["a"] = storageconfig.DatabaseConfig{Backend: "default_mongo"}
	duplicate, err := New(ctx, cfg, backends)
	require.NoError(t, err)
	status, err := duplicate.Inspect(ctx)
	require.NoError(t, err)
	assert.Equal(t, first.Specification, status.Specification)
	assert.Len(t, duplicate.placements, 3)
	pg := cfg.Backends["default_postgres"]
	parsed, err := url.Parse(pg.Postgres.DSN)
	require.NoError(t, err)
	if parsed.Scheme == "postgres" || parsed.Scheme == "postgresql" {
		parsed.User = url.UserPassword("rotated-user", "rotated-secret")
		pg.Postgres.DSN = parsed.String()
	} else {
		pg.Postgres.DSN += " password='rotated-secret'"
	}
	cfg.Backends["default_postgres"] = pg
	credentials, err := New(ctx, cfg, backends)
	require.NoError(t, err)
	assert.Equal(t, first.Specification, credentials.specification)
	if parsed.Scheme == "postgres" || parsed.Scheme == "postgresql" {
		parsed.Host = "another.example:5432"
		pg.Postgres.DSN = parsed.String()
	} else {
		pg.Postgres.DSN += " host=another.example"
	}
	cfg.Backends["default_postgres"] = pg
	differentEndpoint, err := New(ctx, cfg, backends)
	require.NoError(t, err)
	assert.NotEqual(t, first.Specification, differentEndpoint.specification)
	cfg.Backends["default_postgres"] = c.cfg.Backends["default_postgres"]
	cfg.Topology.Document.SysCollection = "different_sys"
	changed, err := New(ctx, cfg, backends)
	require.NoError(t, err)
	status, err = changed.Inspect(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, first.Specification, status.Specification)
	cfg.Topology.Document.SysCollection = cfg.Topology.Document.DataCollection
	_, err = New(ctx, cfg, backends)
	require.ErrorIs(t, err, ErrConflict)
	cfg.Topology.Document.SysCollection = "sys"
	cfg.Topology.Document.Strategy = "invalid"
	_, err = New(ctx, cfg, backends)
	require.Error(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = c.Inspect(canceled)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), strings.TrimSpace(cfg.Backends["default_postgres"].Postgres.DSN))
}

func TestInitializationNeverAdoptsTablesLaterInSearchPath(t *testing.T) {
	t.Parallel()
	ctx, cfg, backends, c := fixture(t)
	db := backends.PostgresDB()
	db.SetMaxOpenConns(1)
	fallback := c.postgresSchema + "_fallback"
	_, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(fallback))
	require.NoError(t, err)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := db.ExecContext(clean, "DROP SCHEMA "+pq.QuoteIdentifier(fallback)+" CASCADE")
		assert.NoError(t, err)
	})
	require.NoError(t, databasepg.EnsureSchemaIn(ctx, db, fallback))
	require.NoError(t, identitypg.EnsureSchemaIn(ctx, db, fallback))
	_, err = db.ExecContext(ctx, "SET search_path = "+pq.QuoteIdentifier(c.postgresSchema)+", "+pq.QuoteIdentifier(fallback))
	require.NoError(t, err)
	var before int64
	require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('auth_users')::oid").Scan(&before))
	coordinator, err := New(ctx, cfg, backends)
	require.NoError(t, err)
	status, err := coordinator.Inspect(ctx)
	require.NoError(t, err)
	require.Equal(t, Preparing, status.State)
	for _, o := range status.Observations {
		if o.Definition == "auth_users" || o.Definition == "databases" {
			assert.Equal(t, "missing", o.State)
			assert.Equal(t, c.postgresSchema, o.Collection)
		}
	}
	prepared, err := coordinator.PrepareFresh(ctx)
	require.NoError(t, err)
	assert.Equal(t, Prepared, prepared.State)
	var target, unchanged int64
	require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('auth_users')::oid").Scan(&target))
	assert.NotEqual(t, before, target)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass($1)::oid", pq.QuoteIdentifier(fallback)+".auth_users").Scan(&unchanged))
	assert.Equal(t, before, unchanged)
	_, err = db.ExecContext(ctx, "SET search_path = "+pq.QuoteIdentifier(c.postgresSchema))
	require.NoError(t, err)
	withoutFallback, err := New(ctx, cfg, backends)
	require.NoError(t, err)
	assert.Equal(t, coordinator.specification, withoutFallback.specification)
}

func TestPreparationFailureKeepsObservedDefinitionsAndBorrowedConnections(t *testing.T) {
	t.Parallel()
	ctx, _, backends, c := fixture(t)
	prepared, err := c.PrepareFresh(ctx)
	require.NoError(t, err)
	tx, err := backends.PostgresDB().BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x53594e5452495801))
	require.NoError(t, err)
	bounded, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := c.Prepare(bounded); result <- err }()
	require.EventuallyWithT(t, func(check *assert.CollectT) {
		var waiting int
		err := backends.PostgresDB().QueryRowContext(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND wait_event='advisory'", c.postgresSchema).Scan(&waiting)
		require.NoError(check, err)
		assert.Equal(check, 1, waiting)
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err = <-result:
	case <-ctx.Done():
		t.Fatal("preparation did not settle after cancellation")
	}
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, "initialization prepare failed", err.Error())
	require.NoError(t, tx.Rollback())
	observed, err := c.Inspect(ctx)
	require.NoError(t, err)
	assert.Equal(t, prepared, observed)
	assert.NoError(t, backends.PostgresDB().PingContext(ctx))
}
