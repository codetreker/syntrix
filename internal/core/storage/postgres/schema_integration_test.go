package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	databasepg "github.com/syntrixbase/syntrix/internal/core/database/postgres"
	userpg "github.com/syntrixbase/syntrix/internal/core/storage/postgres"
	"github.com/syntrixbase/syntrix/internal/core/storage/postgres/schema"
)

const initializationLock int64 = 0x53594e5452495801

type schemaFixture struct {
	ctx  context.Context
	db   *sql.DB
	dsn  string
	name string
}

func newSchemaFixture(t *testing.T) schemaFixture {
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
	require.NoError(t, admin.PingContext(ctx))
	name := fmt.Sprintf("schema_init_%d", time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(name))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, err := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+pq.QuoteIdentifier(name)+" CASCADE")
		require.NoError(t, err)
	})
	f := schemaFixture{ctx: ctx, dsn: dsn, name: name}
	f.db = f.pool(t, false)
	return f
}

func (f schemaFixture) pool(t *testing.T, repeatableRead bool) *sql.DB {
	t.Helper()
	params := map[string]string{"search_path": f.name, "application_name": f.name}
	if repeatableRead {
		params["default_transaction_isolation"] = "repeatable read"
	}
	dsn := f.dsn
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		q := u.Query()
		for key, value := range params {
			q.Set(key, value)
		}
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		for key, value := range params {
			dsn += " " + key + "='" + value + "'"
		}
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.PingContext(f.ctx))
	return db
}

func (f schemaFixture) holdLock(t *testing.T) *sql.Tx {
	t.Helper()
	tx, err := f.db.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = tx.ExecContext(f.ctx, "SELECT pg_advisory_xact_lock($1)", initializationLock)
	require.NoError(t, err)
	return tx
}

func (f schemaFixture) waitForWaiters(t *testing.T, observer *sql.DB, count int) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var waiting int
		err := observer.QueryRowContext(f.ctx, `
			SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a USING (pid)
			WHERE a.application_name = $1 AND l.locktype = 'advisory'
			AND NOT l.granted AND l.classid = $2 AND l.objid = $3 AND l.objsubid = 1`,
			f.name, initializationLock>>32, initializationLock&0xffffffff).Scan(&waiting)
		require.NoError(c, err)
		require.Equal(c, count, waiting)
	}, 5*time.Second, 10*time.Millisecond)
}

func (f schemaFixture) assertIndexes(t *testing.T) {
	t.Helper()
	rows, err := f.db.QueryContext(f.ctx, `
		SELECT indexname FROM pg_indexes WHERE schemaname = $1 ORDER BY indexname`, f.name)
	require.NoError(t, err)
	defer rows.Close()
	var indexes []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		indexes = append(indexes, name)
	}
	require.NoError(t, rows.Err())
	require.ElementsMatch(t, []string{
		"auth_users_pkey", "idx_auth_users_username", "idx_auth_users_disabled",
		"databases_pkey", "databases_slug_key", "idx_databases_owner", "idx_databases_status",
		"idx_databases_created_at", "idx_databases_owner_status", "idx_databases_slug",
	}, indexes)
	var invalid int
	require.NoError(t, f.db.QueryRowContext(f.ctx, `
		SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND (NOT i.indisvalid OR NOT i.indisready)`, f.name).Scan(&invalid))
	require.Zero(t, invalid)
}

func TestSchemaConcurrentInitialization(t *testing.T) {
	f := newSchemaFixture(t)
	const instances = 16
	results := make(chan error, instances)
	start := make(chan struct{})
	for i := 0; i < instances; i++ {
		db := f.pool(t, false)
		go func() {
			<-start
			err := userpg.EnsureSchema(f.ctx, db)
			if err == nil {
				err = databasepg.EnsureSchema(f.ctx, db)
			}
			results <- err
		}()
	}
	close(start)
	var initializationErrors []error
	for i := 0; i < instances; i++ {
		select {
		case err := <-results:
			if err != nil {
				initializationErrors = append(initializationErrors, err)
			}
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
	}
	require.Empty(t, initializationErrors, "all independent initializers must succeed")
	require.NoError(t, userpg.EnsureSchema(f.ctx, f.db))
	require.NoError(t, databasepg.EnsureSchema(f.ctx, f.db))
	f.assertIndexes(t)
}

func TestSchemaIndexesShareInitializationLock(t *testing.T) {
	f := newSchemaFixture(t)
	require.NoError(t, userpg.EnsureSchema(f.ctx, f.db))
	require.NoError(t, databasepg.EnsureSchema(f.ctx, f.db))
	_, err := f.db.ExecContext(f.ctx, "DROP INDEX idx_auth_users_username, idx_auth_users_disabled")
	require.NoError(t, err)
	observer := f.pool(t, false)
	holder := f.holdLock(t)
	results := make(chan error, 3)
	indexDB, schemaDB, databaseDB := f.pool(t, false), f.pool(t, false), f.pool(t, false)
	go func() { results <- userpg.NewUserStore(indexDB, "").EnsureIndexes(f.ctx) }()
	go func() { results <- userpg.EnsureSchema(f.ctx, schemaDB) }()
	go func() { results <- databasepg.EnsureSchema(f.ctx, databaseDB) }()
	f.waitForWaiters(t, observer, 3)
	require.NoError(t, holder.Commit())
	for i := 0; i < 3; i++ {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
	}
	f.assertIndexes(t)
}

func TestSchemaLockWaitCancellation(t *testing.T) {
	f := newSchemaFixture(t)
	observer, waiter := f.pool(t, false), f.pool(t, false)
	holder := f.holdLock(t)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- userpg.EnsureSchema(ctx, waiter) }()
	f.waitForWaiters(t, observer, 1)
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("canceled initializer did not release its connection")
	}
	var table sql.NullString
	require.NoError(t, observer.QueryRowContext(f.ctx, "SELECT to_regclass('auth_users')::text").Scan(&table))
	require.False(t, table.Valid)
	require.NoError(t, holder.Commit())
	require.NoError(t, userpg.EnsureSchema(f.ctx, waiter))
}

func TestSchemaFailureRollsBackAndReleasesLock(t *testing.T) {
	f := newSchemaFixture(t)
	err := schema.Ensure(f.ctx, f.db,
		"CREATE TABLE rolled_back (id integer)",
		"CREATE INDEX invalid_index ON rolled_back (missing_column)")
	var pgErr *pq.Error
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, pq.ErrorCode("42703"), pgErr.Code)
	var table sql.NullString
	require.NoError(t, f.db.QueryRowContext(f.ctx, "SELECT to_regclass('rolled_back')::text").Scan(&table))
	require.False(t, table.Valid)
	other := f.pool(t, false)
	require.NoError(t, schema.Ensure(f.ctx, other, "CREATE TABLE rolled_back (id integer)"))
}

func TestSchemaWaiterUsesReadCommitted(t *testing.T) {
	f := newSchemaFixture(t)
	observer, waiter := f.pool(t, false), f.pool(t, true)
	var isolation string
	require.NoError(t, waiter.QueryRowContext(f.ctx, "SHOW transaction_isolation").Scan(&isolation))
	require.Equal(t, "repeatable read", isolation)
	holder := f.holdLock(t)
	_, err := holder.ExecContext(f.ctx, "CREATE TABLE committed_after_wait (id integer)")
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() {
		result <- schema.Ensure(f.ctx, waiter,
			"CREATE TABLE IF NOT EXISTS committed_after_wait (id integer)",
			"CREATE TABLE observed_isolation AS SELECT current_setting('transaction_isolation') AS isolation")
	}()
	f.waitForWaiters(t, observer, 1)
	require.NoError(t, holder.Commit())
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	require.NoError(t, waiter.QueryRowContext(f.ctx, "SELECT isolation FROM observed_isolation").Scan(&isolation))
	require.Equal(t, "read committed", isolation)
}
