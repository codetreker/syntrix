package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage"
	storageconfig "github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/identity/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runtimeFixture(t *testing.T) (context.Context, *storage.Backends, config.Config, storageconfig.Config) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	persistence := storageconfig.DefaultConfig()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn != "" {
		backend := persistence.Backends["default_postgres"]
		backend.Postgres.DSN = dsn
		persistence.Backends["default_postgres"] = backend
	}
	backends, err := storage.NewBackends(ctx, persistence)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backends.Close()) })
	cfg := config.DefaultConfig()
	cfg.AuthN.PrivateKeyFile = filepath.Join(t.TempDir(), "private.pem")
	return ctx, backends, cfg, persistence
}

func TestNewModuleBorrowsConnectionsWithoutDocuments(t *testing.T) {
	ctx, backends, cfg, persistence := runtimeFixture(t)
	persistence.Topology.Document.Strategy = "invalid-document-strategy"
	persistence.Topology.Document.Primary = "missing-document-source"
	persistence.Topology.User.Strategy = "read_write_split"
	persistence.Topology.User.Replica = "missing-user-replica"
	module, err := NewModule(ctx, cfg, persistence, backends)
	require.NoError(t, err)
	require.NotNil(t, module)
	assert.NotNil(t, module.Accounts())
	assert.NotNil(t, module.Verifier())
	assert.NotNil(t, module.SystemTokenIssuer())
	_, hasClose := reflect.TypeOf(module).MethodByName("Close")
	assert.False(t, hasClose)
	_, hasStart := reflect.TypeOf(module).MethodByName("Start")
	assert.False(t, hasStart)
	_, hasStop := reflect.TypeOf(module).MethodByName("Stop")
	assert.False(t, hasStop)
	require.NoError(t, backends.PostgresDB().PingContext(ctx))
	client, _, err := backends.GetMongoClient("default_mongo")
	require.NoError(t, err)
	require.NoError(t, client.Ping(ctx, nil))
}

func TestNewModuleInitializationFailureKeepsBorrowedConnections(t *testing.T) {
	ctx, backends, cfg, persistence := runtimeFixture(t)
	for _, change := range []func(*config.Config, *storageconfig.Config){
		func(_ *config.Config, p *storageconfig.Config) { p.Topology.Revocation.Primary = "missing" },
		func(_ *config.Config, p *storageconfig.Config) { p.Topology.Revocation.Strategy = "unknown" },
		func(_ *config.Config, p *storageconfig.Config) {
			p.Topology.Revocation.Strategy = "read_write_split"
			p.Topology.Revocation.Replica = "missing"
		},
		func(_ *config.Config, p *storageconfig.Config) {
			p.Databases = map[string]storageconfig.DatabaseConfig{"default": {Backend: "default_mongo"}, "other": {Backend: "missing"}}
		},
		func(c *config.Config, _ *storageconfig.Config) { c.AuthN.PrivateKeyFile = t.TempDir() },
	} {
		currentCfg, currentPersistence := cfg, persistence
		change(&currentCfg, &currentPersistence)
		module, err := NewModule(ctx, currentCfg, currentPersistence, backends)
		assert.Nil(t, module)
		assert.Error(t, err)
		require.NoError(t, backends.PostgresDB().PingContext(ctx))
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	module, err := NewModule(canceled, cfg, persistence, backends)
	assert.Nil(t, module)
	assert.ErrorIs(t, err, context.Canceled)
	require.NoError(t, backends.PostgresDB().PingContext(ctx))
}

func TestNewModuleRevocationTopologies(t *testing.T) {
	ctx, backends, cfg, persistence := runtimeFixture(t)
	persistence.Topology.Revocation.Strategy = "read_write_split"
	persistence.Topology.Revocation.Replica = "default_mongo"
	persistence.Databases["alternate"] = storageconfig.DatabaseConfig{Backend: "default_mongo"}
	module, err := NewModule(ctx, cfg, persistence, backends)
	require.NoError(t, err)
	token, err := module.SystemTokenIssuer().GenerateSystemToken("runtime")
	require.NoError(t, err)
	actor, err := module.Verifier().VerifyToken(token)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("system:%s", "runtime"), actor.Claims().Subject)
}
