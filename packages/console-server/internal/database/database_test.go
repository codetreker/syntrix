package database

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/codetreker/syntrix/console-server/internal/database/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenRejectsConfigurationBeforeConnecting(t *testing.T) {
	db, err := Open(context.Background(), config.DefaultConfig())
	assert.Nil(t, db)
	assert.ErrorContains(t, err, "database.dsn is required")
}

func TestOpenPropagatesCanceledConnection(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DSN = "postgres://developer:password@127.0.0.1:5432/console?sslmode=disable"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db, err := Open(ctx, cfg)
	assert.Nil(t, db)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorContains(t, err, "connect Console PostgreSQL")
}

func TestOpenRejectsMalformedDSN(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DSN = "host='unterminated"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	db, err := Open(ctx, cfg)
	assert.Nil(t, db)
	assert.Error(t, err)
}

func TestPostgresOpenReturnsUsableConfiguredPool(t *testing.T) {
	dsn := os.Getenv("CONSOLE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONSOLE_TEST_POSTGRES_DSN is required for PostgreSQL integration tests")
	}
	cfg := config.DefaultConfig()
	cfg.DSN = dsn
	cfg.MaxOpenConnections = 4
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := Open(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	assert.Equal(t, 4, db.Stats().MaxOpenConnections)
	var one int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT 1").Scan(&one))
	assert.Equal(t, 1, one)
}
