package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage/postgres/schema"
	userpg "github.com/codetreker/syntrix/internal/identity/repository/postgres"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestSchemaSpecificationMatchesOwnedUsers(t *testing.T) {
	for _, tc := range []struct {
		name, mutation, state string
	}{
		{name: "prepared schema", state: "present"},
		{name: "missing table", mutation: "DROP TABLE auth_users", state: "missing"},
		{name: "missing index", mutation: "DROP INDEX idx_auth_users_username", state: "missing"},
		{name: "missing constraint", mutation: "ALTER TABLE auth_users DROP CONSTRAINT auth_users_pkey", state: "missing"},
		{name: "column nullability", mutation: "ALTER TABLE auth_users ALTER COLUMN username DROP NOT NULL", state: "conflict"},
		{name: "column default", mutation: "ALTER TABLE auth_users ALTER COLUMN disabled SET DEFAULT true", state: "conflict"},
		{name: "column type", mutation: "ALTER TABLE auth_users ALTER COLUMN username TYPE TEXT", state: "conflict"},
		{name: "index uniqueness", mutation: "DROP INDEX idx_auth_users_username; CREATE INDEX idx_auth_users_username ON auth_users(username)", state: "conflict"},
		{name: "index key", mutation: "DROP INDEX idx_auth_users_username; CREATE UNIQUE INDEX idx_auth_users_username ON auth_users(password_hash)", state: "conflict"},
		{name: "index predicate", mutation: "DROP INDEX idx_auth_users_disabled; CREATE INDEX idx_auth_users_disabled ON auth_users(disabled) WHERE disabled = false", state: "conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSchemaFixture(t)
			require.NoError(t, userpg.EnsureSchema(f.ctx, f.db))
			state, err := schema.Inspect(f.ctx, f.db, userpg.SchemaSpecification())
			require.NoError(t, err)
			require.Equal(t, "present", state)
			if tc.mutation != "" {
				_, err := f.db.ExecContext(f.ctx, tc.mutation)
				require.NoError(t, err)
			}
			state, err = schema.Inspect(f.ctx, f.db, userpg.SchemaSpecification())
			require.NoError(t, err)
			require.Equal(t, tc.state, state)
		})
	}
}

func TestEnsureSchemaInDoesNotAdoptSearchPathFallback(t *testing.T) {
	f := newSchemaFixture(t)
	target, fallback := f.name+"_Target \"owned\"", f.name+"_fallback"
	for _, namespace := range []string{target, fallback} {
		_, err := f.db.ExecContext(f.ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(namespace))
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_, err := f.db.ExecContext(cleanupCtx, "DROP SCHEMA "+pq.QuoteIdentifier(namespace)+" CASCADE")
			require.NoError(t, err)
		})
	}
	require.NoError(t, userpg.EnsureSchemaIn(f.ctx, f.db, fallback))
	_, err := f.db.ExecContext(f.ctx, "INSERT INTO "+pq.QuoteIdentifier(fallback)+".auth_users(id, username, password_hash) VALUES ('sentinel', 'fallback', 'unused')")
	require.NoError(t, err)
	searchPath := pq.QuoteIdentifier(target) + ", " + pq.QuoteIdentifier(fallback)
	_, err = f.db.ExecContext(f.ctx, "SELECT set_config('search_path', $1, false)", searchPath)
	require.NoError(t, err)
	var resolved string
	require.NoError(t, f.db.QueryRowContext(f.ctx, "SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=to_regclass('auth_users')").Scan(&resolved))
	require.Equal(t, fallback, resolved)
	specification := userpg.SchemaSpecification()
	specification.Namespace = target
	state, err := schema.Inspect(f.ctx, f.db, specification)
	require.NoError(t, err)
	require.Equal(t, "missing", state)
	require.NoError(t, userpg.EnsureSchemaIn(f.ctx, f.db, target))
	state, err = schema.Inspect(f.ctx, f.db, specification)
	require.NoError(t, err)
	require.Equal(t, "present", state)
	var activePath string
	require.NoError(t, f.db.QueryRowContext(f.ctx, "SHOW search_path").Scan(&activePath))
	require.Equal(t, searchPath, activePath)
	for _, tc := range []struct {
		namespace string
		count     int
	}{{target, 0}, {fallback, 1}} {
		var count int
		require.NoError(t, f.db.QueryRowContext(f.ctx, "SELECT count(*) FROM "+pq.QuoteIdentifier(tc.namespace)+".auth_users").Scan(&count))
		require.Equal(t, tc.count, count)
		specification.Namespace = tc.namespace
		state, err = schema.Inspect(f.ctx, f.db, specification)
		require.NoError(t, err)
		require.Equal(t, "present", state)
	}
	require.Error(t, userpg.EnsureSchemaIn(f.ctx, f.db, f.name+"_absent"))
	require.NoError(t, f.db.PingContext(f.ctx))
}
