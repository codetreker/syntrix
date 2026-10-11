package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codetreker/syntrix/console-server/internal/auth/config"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func integrationDB(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	dsn := os.Getenv("CONSOLE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONSOLE_TEST_POSTGRES_DSN is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	require.NoError(t, admin.PingContext(ctx))
	suffix, _, err := newToken()
	require.NoError(t, err)
	schemaName := "console_auth_" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(suffix[:12], "-", "x"), "_", "x"))
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schemaName))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, err := admin.ExecContext(cleanupCtx, "DROP SCHEMA "+pq.QuoteIdentifier(schemaName)+" CASCADE")
		require.NoError(t, err)
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		require.NoError(t, err)
		query := parsed.Query()
		query.Set("search_path", schemaName)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=" + schemaName
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(5)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db, ctx
}

func integrationService(t *testing.T) (*Service, *sql.DB, context.Context) {
	t.Helper()
	db, ctx := integrationDB(t)
	service, err := NewService(db, testConfig())
	require.NoError(t, err)
	require.NoError(t, service.Initialize(ctx))
	return service, db, ctx
}

func TestPostgresLoginSessionLifecycle(t *testing.T) {
	service, db, ctx := integrationService(t)
	tests := []struct {
		name     string
		username string
		password string
		allowed  bool
	}{
		{name: "configured account", username: "alice", password: "alice-password", allowed: true},
		{name: "normalized username", username: " ALICE ", password: "alice-password", allowed: true},
		{name: "unknown account", username: "missing", password: "alice-password"},
		{name: "empty username", password: "alice-password"},
		{name: "NUL username", username: "alice\x00", password: "alice-password"},
		{name: "invalid UTF-8 username", username: "alice\xff", password: "alice-password"},
		{name: "long username", username: strings.Repeat("x", 129), password: "alice-password"},
		{name: "wrong password", username: "alice", password: "wrong"},
		{name: "empty password", username: "alice"},
		{name: "too long password", username: "alice", password: strings.Repeat("x", 73)},
		{name: "unknown account long password", username: "missing", password: strings.Repeat("x", 73)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, token, err := service.Login(ctx, test.username, test.password)
			if !test.allowed {
				assert.ErrorIs(t, err, ErrUnauthorized)
				assert.Empty(t, token)
				assert.Empty(t, session)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, Developer{ID: "dev_alice", Username: "alice"}, session.Developer)
			assert.Len(t, token, 43)
			assert.Len(t, session.CSRFToken, 43)
			assert.NotEqual(t, token, session.CSRFToken)
			assert.WithinDuration(t, time.Now().Add(24*time.Hour), session.ExpiresAt, 5*time.Second)
			var persistedHash []byte
			var persistedCSRF string
			require.NoError(t, db.QueryRowContext(ctx, "SELECT token_hash, csrf_token FROM console_sessions WHERE csrf_token = $1", session.CSRFToken).Scan(&persistedHash, &persistedCSRF))
			expectedHash := sha256.Sum256([]byte(token))
			assert.Equal(t, expectedHash[:], persistedHash)
			assert.NotEqual(t, []byte(token), persistedHash)
			assert.Equal(t, session.CSRFToken, persistedCSRF)
			actual, err := service.Authenticate(ctx, token)
			require.NoError(t, err)
			assert.Equal(t, session, actual)
			restarted, err := NewService(db, testConfig())
			require.NoError(t, err)
			require.NoError(t, restarted.Initialize(ctx))
			actual, err = restarted.Authenticate(ctx, token)
			require.NoError(t, err)
			assert.Equal(t, session, actual)
			require.NoError(t, restarted.Logout(ctx, token))
			require.NoError(t, restarted.Logout(ctx, token))
			_, err = service.Authenticate(ctx, token)
			assert.ErrorIs(t, err, ErrUnauthorized)
		})
	}
}

func TestPostgresExpiredSessionRejectedAndPruned(t *testing.T) {
	service, db, ctx := integrationService(t)
	_, token, err := service.Login(ctx, "alice", "alice-password")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "UPDATE console_sessions SET expires_at = CURRENT_TIMESTAMP - INTERVAL '1 second'")
	require.NoError(t, err)
	_, err = service.Authenticate(ctx, token)
	assert.ErrorIs(t, err, ErrUnauthorized)
	require.NoError(t, service.PruneExpiredSessions(ctx))
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM console_sessions").Scan(&count))
	assert.Zero(t, count)
}

func TestPostgresBootstrapPreservesCredentialOwnership(t *testing.T) {
	service, db, ctx := integrationService(t)
	var originalHash string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT password_hash FROM console_developers WHERE id = 'dev_alice'").Scan(&originalHash))
	assert.NotEqual(t, "alice-password", originalHash)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(originalHash), []byte("alice-password")))
	cfg := testConfig()
	cfg.Developers[0].Password = "changed-config-password"
	cfg.Developers = append(cfg.Developers, config.Developer{ID: "dev_bob", Username: "Bob", Password: "bob-password"})
	restarted, err := NewService(db, cfg)
	require.NoError(t, err)
	require.NoError(t, restarted.Initialize(ctx))
	var actualHash string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT password_hash FROM console_developers WHERE id = 'dev_alice'").Scan(&actualHash))
	assert.Equal(t, originalHash, actualHash)
	_, _, err = restarted.Login(ctx, "alice", "alice-password")
	require.NoError(t, err)
	_, _, err = restarted.Login(ctx, "alice", "changed-config-password")
	assert.ErrorIs(t, err, ErrUnauthorized)
	bob, _, err := restarted.Login(ctx, "bob", "bob-password")
	require.NoError(t, err)
	assert.Equal(t, "dev_bob", bob.Developer.ID)
	_, _, err = service.Login(ctx, "bob", "bob-password")
	require.NoError(t, err, "runtime accounts come from PostgreSQL, not the old process configuration")
}

func TestPostgresBootstrapConflictsAreAtomic(t *testing.T) {
	for _, test := range []struct {
		name      string
		developer config.Developer
		want      string
	}{
		{name: "existing ID changed username", developer: config.Developer{ID: "dev_alice", Username: "mallory", Password: "password"}, want: "username conflicts"},
		{name: "username rebound to another ID", developer: config.Developer{ID: "dev_mallory", Username: "ALICE", Password: "password"}, want: "belongs to another"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, db, ctx := integrationService(t)
			cfg := config.DefaultConfig()
			cfg.Developers = []config.Developer{
				{ID: "dev_bob", Username: "bob", Password: "bob-password"},
				test.developer,
			}
			service, err := NewService(db, cfg)
			require.NoError(t, err)
			assert.ErrorContains(t, service.Initialize(ctx), test.want)
			var count int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM console_developers").Scan(&count))
			assert.Equal(t, 1, count, "the earlier insertion must roll back with the conflicting account")
		})
	}
}

func TestPostgresConcurrentBootstrap(t *testing.T) {
	db, ctx := integrationDB(t)
	first, err := NewService(db, testConfig())
	require.NoError(t, err)
	second, err := NewService(db, testConfig())
	require.NoError(t, err)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, service := range []*Service{first, second} {
		wg.Add(1)
		go func(service *Service) {
			defer wg.Done()
			errors <- service.Initialize(ctx)
		}(service)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM console_developers").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestPostgresCorruptCredentialIsOperationalError(t *testing.T) {
	service, db, ctx := integrationService(t)
	_, err := db.ExecContext(ctx, "UPDATE console_developers SET password_hash = 'corrupt'")
	require.NoError(t, err)
	_, _, err = service.Login(ctx, "alice", "alice-password")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrUnauthorized)
	assert.ErrorContains(t, err, "verify developer credentials")
}

func TestPostgresCanceledInitializationDoesNotCommit(t *testing.T) {
	db, ctx := integrationDB(t)
	service, err := NewService(db, testConfig())
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	assert.ErrorIs(t, service.Initialize(canceled), context.Canceled)
	require.NoError(t, service.Initialize(ctx))
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM console_developers").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestPostgresConfigurationCopyIsImmutable(t *testing.T) {
	db, ctx := integrationDB(t)
	cfg := testConfig()
	service, err := NewService(db, cfg)
	require.NoError(t, err)
	cfg.Developers[0].ID = "changed-id"
	require.NoError(t, service.Initialize(ctx))
	session, _, err := service.Login(ctx, "alice", "alice-password")
	require.NoError(t, err)
	assert.Equal(t, "dev_alice", session.Developer.ID)
}

func TestPostgresInitializationRejectsBrokenSchema(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture string
		want    string
		code    pq.ErrorCode
	}{
		{
			name:    "session table missing expiry column",
			fixture: "CREATE TABLE console_sessions (token_hash BYTEA PRIMARY KEY)",
			want:    "create auth schema",
			code:    "42703",
		},
		{
			name:    "developer table missing account identifier",
			fixture: "CREATE TABLE console_developers (username TEXT NOT NULL UNIQUE)",
			want:    "read bootstrap developer",
			code:    "42703",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, ctx := integrationDB(t)
			_, err := db.ExecContext(ctx, test.fixture)
			require.NoError(t, err)
			service, err := NewService(db, testConfig())
			require.NoError(t, err)
			err = service.Initialize(ctx)
			assert.ErrorContains(t, err, test.want)
			var databaseError *pq.Error
			require.ErrorAs(t, err, &databaseError)
			assert.Equal(t, test.code, databaseError.Code)
			var unrelatedTable *string
			if test.want == "create auth schema" {
				require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('console_developers')::TEXT").Scan(&unrelatedTable))
			} else {
				require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('console_sessions')::TEXT").Scan(&unrelatedTable))
			}
			assert.Nil(t, unrelatedTable, "schema changes made before the error must roll back")
		})
	}
}

func TestPostgresBootstrapStorageRejectionsAreAtomic(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture string
		want    string
		code    pq.ErrorCode
	}{
		{
			name: "insert constraint rejection",
			fixture: `CREATE TABLE console_developers (
				id TEXT PRIMARY KEY CHECK (id = 'unconfigured-id'),
				username TEXT NOT NULL UNIQUE,
				password_hash TEXT NOT NULL
			)`,
			want: "insert bootstrap developer",
			code: "23514",
		},
		{
			name: "deferred commit rejection",
			fixture: `CREATE TABLE console_developers (
				id TEXT PRIMARY KEY,
				username TEXT NOT NULL UNIQUE,
				password_hash TEXT NOT NULL
			);
			CREATE FUNCTION reject_bootstrap_commit() RETURNS TRIGGER LANGUAGE plpgsql AS $$
			BEGIN
				RAISE EXCEPTION 'account persistence rejected' USING ERRCODE = '23514';
			END;
			$$;
			CREATE CONSTRAINT TRIGGER reject_bootstrap_commit
			AFTER INSERT ON console_developers DEFERRABLE INITIALLY DEFERRED
			FOR EACH ROW EXECUTE FUNCTION reject_bootstrap_commit()`,
			want: "commit auth initialization",
			code: "23514",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, ctx := integrationDB(t)
			_, err := db.ExecContext(ctx, test.fixture)
			require.NoError(t, err)
			service, err := NewService(db, testConfig())
			require.NoError(t, err)
			err = service.Initialize(ctx)
			assert.ErrorContains(t, err, test.want)
			var databaseError *pq.Error
			require.ErrorAs(t, err, &databaseError)
			assert.Equal(t, test.code, databaseError.Code)
			var count int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM console_developers").Scan(&count))
			assert.Zero(t, count)
			var sessionTable *string
			require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass('console_sessions')::TEXT").Scan(&sessionTable))
			assert.Nil(t, sessionTable, "failed initialization must leave no partial session schema")
		})
	}
}

func TestPostgresInitializationCancellationWhileWaitingForAnotherProcess(t *testing.T) {
	db, ctx := integrationDB(t)
	blocker, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = blocker.Rollback() })
	_, err = blocker.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x434f4e41555448))
	require.NoError(t, err)
	service, err := NewService(db, testConfig())
	require.NoError(t, err)
	waiting, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	err = service.Initialize(waiting)
	assert.ErrorContains(t, err, "lock auth initialization")
	require.NoError(t, blocker.Rollback())
	require.NoError(t, service.Initialize(ctx), "canceled initialization must release its transaction and allow a later startup")
}

func TestPostgresLoginReturnsNoSessionWhenPersistenceFails(t *testing.T) {
	service, db, ctx := integrationService(t)
	_, err := db.ExecContext(ctx, "ALTER TABLE console_sessions ADD CONSTRAINT reject_session CHECK (FALSE)")
	require.NoError(t, err)
	session, token, err := service.Login(ctx, "alice", "alice-password")
	assert.ErrorContains(t, err, "persist developer session")
	assert.NotErrorIs(t, err, ErrUnauthorized)
	assert.Empty(t, session)
	assert.Empty(t, token)
	var databaseError *pq.Error
	require.ErrorAs(t, err, &databaseError)
	assert.Equal(t, pq.ErrorCode("23514"), databaseError.Code)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM console_sessions").Scan(&count))
	assert.Zero(t, count)
}

func TestPostgresLoginReturnsNoSessionWhenExpiryCleanupFails(t *testing.T) {
	service, db, ctx := integrationService(t)
	_, err := db.ExecContext(ctx, "DROP TABLE console_sessions")
	require.NoError(t, err)
	session, token, err := service.Login(ctx, "alice", "alice-password")
	assert.ErrorContains(t, err, "prune expired developer sessions")
	assert.NotErrorIs(t, err, ErrUnauthorized)
	assert.Empty(t, session)
	assert.Empty(t, token)
	var databaseError *pq.Error
	require.ErrorAs(t, err, &databaseError)
	assert.Equal(t, pq.ErrorCode("42P01"), databaseError.Code)
}
