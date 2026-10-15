package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage"
	storageconfig "github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/core/storage/postgres/schema"
	userpg "github.com/codetreker/syntrix/internal/identity/repository/postgres"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

func identityInitializationFixture(t *testing.T) (context.Context, storageconfig.Config, *storage.Backends) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cfg := storageconfig.DefaultConfig()
	cfg.ApplyEnvOverrides()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		dsn = cfg.Backends["default_postgres"].Postgres.DSN
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	owned := fmt.Sprintf("identity_init_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(owned))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, err := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+pq.QuoteIdentifier(owned)+" CASCADE")
		require.NoError(t, err)
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		q := u.Query()
		q.Set("search_path", owned)
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path='" + owned + "'"
	}
	postgresBackend := cfg.Backends["default_postgres"]
	postgresBackend.Postgres.DSN = dsn
	postgresBackend.Postgres.MaxOpenConns = 1
	postgresBackend.Postgres.MaxIdleConns = 1
	cfg.Backends["default_postgres"] = postgresBackend
	primary := cfg.Backends["default_mongo"]
	primary.Mongo.DatabaseName = owned + "_primary"
	cfg.Backends["default_mongo"] = primary
	cfg.Backends["alias"] = primary
	replica, routed := primary, primary
	replica.Mongo.DatabaseName = owned + "_replica"
	routed.Mongo.DatabaseName = owned + "_routed"
	cfg.Backends["replica"], cfg.Backends["routed"] = replica, routed
	cfg.Topology.Revocation.Strategy = "read_write_split"
	cfg.Topology.Revocation.Replica = "replica"
	cfg.Databases["alias_project"] = storageconfig.DatabaseConfig{Backend: "alias"}
	cfg.Databases["routed_project"] = storageconfig.DatabaseConfig{Backend: "routed"}
	backends, err := storage.NewBackends(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backends.Close()) })
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		for _, name := range []string{"default_mongo", "replica", "routed"} {
			client, database, err := backends.GetMongoClient(name)
			require.NoError(t, err)
			require.NoError(t, client.Database(database).Drop(cleanupCtx))
		}
	})
	return ctx, cfg, backends
}

func assertIdentityBorrowedConnections(t *testing.T, ctx context.Context, backends *storage.Backends) {
	t.Helper()
	require.NoError(t, backends.PostgresDB().PingContext(ctx))
	for _, name := range []string{"default_mongo", "alias", "replica", "routed"} {
		client, _, err := backends.GetMongoClient(name)
		require.NoError(t, err)
		require.NoError(t, client.Ping(ctx, nil))
	}
}

func TestInitializationPlacementsIncludeRoutedRevocationsWithoutAliasDuplicates(t *testing.T) {
	ctx, cfg, backends := identityInitializationFixture(t)
	placements, err := InitializationPlacements(cfg, backends)
	require.NoError(t, err)
	require.Len(t, placements, 3)
	databases := make([]string, 0, len(placements))
	for _, placement := range placements {
		assert.Equal(t, "revocations", placement.Owner)
		assert.Equal(t, cfg.Topology.Revocation.Collection, placement.Collection)
		assert.Empty(t, placement.Required)
		require.Len(t, placement.Cleanup, 1)
		index := placement.Cleanup[0]
		assert.Equal(t, "expires_at_1", index.Name)
		assert.Equal(t, bson.D{{Key: "expires_at", Value: 1}}, index.Keys)
		require.NotNil(t, index.ExpireAfterSeconds)
		assert.Zero(t, *index.ExpireAfterSeconds)
		assert.NotEmpty(t, placement.Backend)
		databases = append(databases, placement.Database)
	}
	require.ElementsMatch(t, []string{
		cfg.Backends["default_mongo"].Mongo.DatabaseName,
		cfg.Backends["replica"].Mongo.DatabaseName,
		cfg.Backends["routed"].Mongo.DatabaseName,
	}, databases)
	assertIdentityBorrowedConnections(t, ctx, backends)
}

func TestPrepareInitializesUsersWithoutActivatingMongoCleanup(t *testing.T) {
	ctx, cfg, backends := identityInitializationFixture(t)
	for i := 0; i < 2; i++ {
		require.NoError(t, Prepare(ctx, cfg, backends))
	}
	state, err := schema.Inspect(ctx, backends.PostgresDB(), userpg.SchemaSpecification())
	require.NoError(t, err)
	assert.Equal(t, "present", state)
	var databases sql.NullString
	require.NoError(t, backends.PostgresDB().QueryRowContext(ctx, "SELECT to_regclass('databases')::text").Scan(&databases))
	assert.False(t, databases.Valid, "Identity must leave the database catalog to its storage owner")
	for _, name := range []string{"default_mongo", "replica", "routed"} {
		client, database, err := backends.GetMongoClient(name)
		require.NoError(t, err)
		collections, err := client.Database(database).ListCollectionNames(ctx, bson.D{})
		require.NoError(t, err)
		assert.Empty(t, collections, "Prepare must not create cleanup-only revocation collections")
	}
	assertIdentityBorrowedConnections(t, ctx, backends)
}

func TestPrepareRejectsInvalidPlacementsBeforeSchemaInitialization(t *testing.T) {
	ctx, cfg, backends := identityInitializationFixture(t)
	for _, tc := range []struct {
		name   string
		change func(*storageconfig.Config)
	}{
		{name: "unsupported strategy", change: func(c *storageconfig.Config) { c.Topology.Revocation.Strategy = "invalid" }},
		{name: "missing primary", change: func(c *storageconfig.Config) { c.Topology.Revocation.Primary = "missing" }},
		{name: "missing replica", change: func(c *storageconfig.Config) { c.Topology.Revocation.Replica = "missing" }},
		{name: "missing routed backend", change: func(c *storageconfig.Config) {
			c.Databases["broken"] = storageconfig.DatabaseConfig{Backend: "missing"}
		}},
		{name: "PostgreSQL revocation backend", change: func(c *storageconfig.Config) { c.Topology.Revocation.Primary = "default_postgres" }},
		{name: "invalid Mongo URI", change: func(c *storageconfig.Config) {
			b := c.Backends["default_mongo"]
			b.Mongo.URI = "mongodb://identity:private-password@invalid:port"
			c.Backends["default_mongo"] = b
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := cfg
			current.Backends = make(map[string]storageconfig.BackendConfig, len(cfg.Backends))
			for name, backend := range cfg.Backends {
				current.Backends[name] = backend
			}
			current.Databases = make(map[string]storageconfig.DatabaseConfig, len(cfg.Databases))
			for name, database := range cfg.Databases {
				current.Databases[name] = database
			}
			tc.change(&current)
			_, err := InitializationPlacements(current, backends)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-password")
			err = Prepare(ctx, current, backends)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private-password")
			state, err := schema.Inspect(ctx, backends.PostgresDB(), userpg.SchemaSpecification())
			require.NoError(t, err)
			assert.Equal(t, "missing", state)
			assertIdentityBorrowedConnections(t, ctx, backends)
		})
	}
}

func TestPrepareFailurePreservesBorrowedConnections(t *testing.T) {
	ctx, cfg, backends := identityInitializationFixture(t)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, Prepare(canceled, cfg, backends), context.Canceled)
	assertIdentityBorrowedConnections(t, ctx, backends)
	_, err := backends.PostgresDB().ExecContext(ctx, "CREATE TABLE auth_users (id TEXT PRIMARY KEY)")
	require.NoError(t, err)
	err = Prepare(ctx, cfg, backends)
	var databaseError *pq.Error
	require.ErrorAs(t, err, &databaseError)
	assert.Equal(t, pq.ErrorCode("42703"), databaseError.Code)
	assertIdentityBorrowedConnections(t, ctx, backends)
	_, err = backends.PostgresDB().ExecContext(ctx, "DROP TABLE auth_users")
	require.NoError(t, err)
	require.NoError(t, Prepare(ctx, cfg, backends))
	state, err := schema.Inspect(ctx, backends.PostgresDB(), userpg.SchemaSpecification())
	require.NoError(t, err)
	assert.Equal(t, "present", state)
}

func TestPrepareInUsesFrozenNamespaceInsteadOfSearchPathFallback(t *testing.T) {
	ctx, cfg, backends := identityInitializationFixture(t)
	db := backends.PostgresDB()
	var initial string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&initial))
	target, fallback := initial+"_Target \"owned\"", initial+"_fallback"
	for _, namespace := range []string{target, fallback} {
		_, err := db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(namespace))
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_, err := db.ExecContext(cleanupCtx, "DROP SCHEMA "+pq.QuoteIdentifier(namespace)+" CASCADE")
			require.NoError(t, err)
		})
	}
	require.NoError(t, userpg.EnsureSchemaIn(ctx, db, fallback))
	_, err := db.ExecContext(ctx, "INSERT INTO "+pq.QuoteIdentifier(fallback)+".auth_users(id, username, password_hash) VALUES ('sentinel', 'fallback', 'unused')")
	require.NoError(t, err)
	searchPath := pq.QuoteIdentifier(target) + ", " + pq.QuoteIdentifier(fallback)
	_, err = db.ExecContext(ctx, "SELECT set_config('search_path', $1, false)", searchPath)
	require.NoError(t, err)
	specification := userpg.SchemaSpecification()
	specification.Namespace = target
	state, err := schema.Inspect(ctx, db, specification)
	require.NoError(t, err)
	require.Equal(t, "missing", state)
	require.NoError(t, PrepareIn(ctx, cfg, backends, target))
	state, err = schema.Inspect(ctx, db, specification)
	require.NoError(t, err)
	require.Equal(t, "present", state)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM "+pq.QuoteIdentifier(fallback)+".auth_users").Scan(&count))
	require.Equal(t, 1, count)
	var activePath string
	require.NoError(t, db.QueryRowContext(ctx, "SHOW search_path").Scan(&activePath))
	require.Equal(t, searchPath, activePath)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, PrepareIn(canceled, cfg, backends, target), context.Canceled)
	invalid := cfg
	invalid.Topology.Revocation.Strategy = "unknown"
	require.Error(t, PrepareIn(ctx, invalid, backends, target))
	require.Error(t, PrepareIn(ctx, cfg, backends, initial+"_absent"))
	assertIdentityBorrowedConnections(t, ctx, backends)
	for _, name := range []string{"default_mongo", "replica", "routed"} {
		client, database, err := backends.GetMongoClient(name)
		require.NoError(t, err)
		collections, err := client.Database(database).ListCollectionNames(ctx, bson.D{})
		require.NoError(t, err)
		require.Empty(t, collections)
	}
}
