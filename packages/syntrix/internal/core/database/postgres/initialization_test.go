package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage/postgres/schema"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func databaseInitializationFixture(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://syntrix:syntrix@localhost:5432/syntrix?sslmode=disable"
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	name := fmt.Sprintf("database_init_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(name))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, err := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+pq.QuoteIdentifier(name)+" CASCADE")
		require.NoError(t, err)
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		q := u.Query()
		q.Set("search_path", name)
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path='" + name + "'"
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return ctx, db
}

func TestSchemaSpecificationMatchesOwnedCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, mutation, state string
	}{
		{name: "prepared schema", state: "present"},
		{name: "missing table", mutation: "DROP TABLE databases", state: "missing"},
		{name: "missing index", mutation: "DROP INDEX idx_databases_owner", state: "missing"},
		{name: "missing check", mutation: "ALTER TABLE databases DROP CONSTRAINT chk_databases_max_documents", state: "missing"},
		{name: "column nullability", mutation: "ALTER TABLE databases ALTER COLUMN owner_id DROP NOT NULL", state: "conflict"},
		{name: "column type", mutation: "ALTER TABLE databases ALTER COLUMN display_name TYPE TEXT", state: "conflict"},
		{name: "column default", mutation: "ALTER TABLE databases ALTER COLUMN max_documents SET DEFAULT 10", state: "conflict"},
		{name: "index direction", mutation: "DROP INDEX idx_databases_created_at; CREATE INDEX idx_databases_created_at ON databases(created_at ASC)", state: "conflict"},
		{name: "index key order", mutation: "DROP INDEX idx_databases_owner_status; CREATE INDEX idx_databases_owner_status ON databases(status, owner_id)", state: "conflict"},
		{name: "index predicate", mutation: "DROP INDEX idx_databases_slug; CREATE INDEX idx_databases_slug ON databases(slug) WHERE slug IS NULL", state: "conflict"},
		{name: "check definition", mutation: "ALTER TABLE databases DROP CONSTRAINT chk_databases_max_documents; ALTER TABLE databases ADD CONSTRAINT chk_databases_max_documents CHECK (max_documents >= 10)", state: "conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, db := databaseInitializationFixture(t)
			require.NoError(t, EnsureSchema(ctx, db))
			state, err := schema.Inspect(ctx, db, SchemaSpecification())
			require.NoError(t, err)
			require.Equal(t, "present", state)
			if tc.mutation != "" {
				_, err := db.ExecContext(ctx, tc.mutation)
				require.NoError(t, err)
			}
			state, err = schema.Inspect(ctx, db, SchemaSpecification())
			require.NoError(t, err)
			require.Equal(t, tc.state, state)
		})
	}
}

func TestEnsureSchemaInDoesNotAdoptSearchPathFallback(t *testing.T) {
	ctx, db := databaseInitializationFixture(t)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
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
	require.NoError(t, EnsureSchemaIn(ctx, db, fallback))
	_, err := db.ExecContext(ctx, "INSERT INTO "+pq.QuoteIdentifier(fallback)+".databases(id, display_name, owner_id) VALUES ('0123456789abcdef', 'fallback sentinel', 'owner')")
	require.NoError(t, err)
	searchPath := pq.QuoteIdentifier(target) + ", " + pq.QuoteIdentifier(fallback)
	_, err = db.ExecContext(ctx, "SELECT set_config('search_path', $1, false)", searchPath)
	require.NoError(t, err)
	var resolved string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=to_regclass('databases')").Scan(&resolved))
	require.Equal(t, fallback, resolved)
	specification := SchemaSpecification()
	specification.Namespace = target
	state, err := schema.Inspect(ctx, db, specification)
	require.NoError(t, err)
	require.Equal(t, "missing", state)
	require.NoError(t, EnsureSchemaIn(ctx, db, target))
	state, err = schema.Inspect(ctx, db, specification)
	require.NoError(t, err)
	require.Equal(t, "present", state)
	var activePath string
	require.NoError(t, db.QueryRowContext(ctx, "SHOW search_path").Scan(&activePath))
	require.Equal(t, searchPath, activePath, "namespace preparation must not alter the borrowed pool's session path")
	for _, tc := range []struct {
		namespace string
		count     int
	}{{target, 0}, {fallback, 1}} {
		var count int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM "+pq.QuoteIdentifier(tc.namespace)+".databases").Scan(&count))
		require.Equal(t, tc.count, count)
		specification.Namespace = tc.namespace
		state, err = schema.Inspect(ctx, db, specification)
		require.NoError(t, err)
		require.Equal(t, "present", state)
	}
	require.Error(t, EnsureSchemaIn(ctx, db, initial+"_absent"))
	require.NoError(t, db.PingContext(ctx))
}
