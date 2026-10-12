package auth

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/codetreker/syntrix/console-server/internal/auth/config"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testConfig() config.Config {
	cfg := config.DefaultConfig()
	cfg.Developers = []config.Developer{{ID: "dev_alice", Username: " Alice ", Password: "alice-password"}}
	return cfg
}

func TestNewServiceRequiresDatabaseAndValidConfig(t *testing.T) {
	_, err := NewService(nil, testConfig())
	assert.ErrorContains(t, err, "PostgreSQL connection is required")
	db, err := sql.Open("postgres", "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = NewService(db, config.Config{})
	assert.ErrorContains(t, err, "session_ttl must")
}

func TestUnavailablePostgreSQLIsNotUnauthorized(t *testing.T) {
	db, err := sql.Open("postgres", "")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	service, err := NewService(db, testConfig())
	require.NoError(t, err)
	token, _, err := newToken()
	require.NoError(t, err)
	ctx := context.Background()
	for _, test := range []struct {
		name string
		call func() error
	}{
		{name: "initialize", call: func() error { return service.Initialize(ctx) }},
		{name: "login", call: func() error { _, _, err := service.Login(ctx, "alice", "alice-password"); return err }},
		{name: "authenticate", call: func() error { _, err := service.Authenticate(ctx, token); return err }},
		{name: "logout", call: func() error { return service.Logout(ctx, token) }},
		{name: "prune", call: func() error { return service.PruneExpiredSessions(ctx) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			assert.Error(t, err)
			assert.NotErrorIs(t, err, ErrUnauthorized)
			assert.ErrorContains(t, err, "database is closed")
		})
	}
}

func TestOpaqueSessionTokens(t *testing.T) {
	token, tokenHash, err := newToken()
	require.NoError(t, err)
	other, _, err := newToken()
	require.NoError(t, err)
	assert.NotEqual(t, token, other)
	hash, valid := hashToken(token)
	assert.True(t, valid)
	assert.Equal(t, tokenHash, hash)
	for _, invalid := range []string{"", strings.Repeat("a", 42), strings.Repeat("!", 43), strings.Repeat("a", 43)} {
		_, valid := hashToken(invalid)
		assert.False(t, valid, "invalid token %q", invalid)
	}
}

func TestInvalidTokenDoesNotRequirePostgreSQL(t *testing.T) {
	service := &Service{}
	_, err := service.Authenticate(context.Background(), "invalid")
	assert.ErrorIs(t, err, ErrUnauthorized)
	assert.NoError(t, service.Logout(context.Background(), "invalid"))
}

func TestPasswordTimingInputUsesBcryptByteLimit(t *testing.T) {
	assert.Equal(t, []byte("password"), passwordForTiming("password"))
	assert.Len(t, passwordForTiming(strings.Repeat("x", 73)), 72)
}
