package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestInitializationPlacementsAndPrepare(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("routed aliases and split deduplicate physical placements", func(mt *mtest.T) {
		cfg := config.DefaultConfig()
		alias := cfg.Backends["default_mongo"]
		cfg.Backends["alias"] = alias
		cfg.Topology.Document.Strategy = "read_write_split"
		cfg.Topology.Document.Replica = "alias"
		cfg.Databases["a"] = config.DatabaseConfig{Backend: "alias"}
		provider := &backendTestProvider{client: mt.Client, dbName: "owned"}
		db, mock, err := sqlmock.New()
		require.NoError(mt, err)
		defer db.Close()
		backends := &Backends{providers: map[string]Provider{"default_mongo": provider, "alias": provider}, postgresDB: db}
		placements, err := InitializationPlacements(cfg, backends)
		require.NoError(mt, err)
		require.Len(mt, placements, 2)
		assert.Equal(mt, "documents", placements[0].Owner)
		assert.NotEmpty(mt, placements[0].Cleanup)
		assert.Empty(mt, placements[1].Cleanup)
		expectSchemaInitialization(mock, "databases")
		for _, p := range placements {
			mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 26, Message: "absent collection"}), mtest.CreateSuccessResponse(), mtest.CreateCursorResponse(0, p.Database+"."+p.Collection, mtest.FirstBatch, bson.D{{Key: "name", Value: "database_1_collection_hash_1"}, {Key: "key", Value: bson.D{{Key: "database", Value: 1}, {Key: "collection_hash", Value: 1}}}}))
		}
		require.NoError(mt, Prepare(context.Background(), cfg, backends))
		require.NoError(mt, mock.ExpectationsWereMet())
		failure := errors.New("database closed")
		mock.ExpectBegin().WillReturnError(failure)
		require.ErrorIs(mt, Prepare(context.Background(), cfg, backends), failure)
		mock.ExpectBegin()
		mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("CREATE TABLE").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "denied"}))
		require.Error(mt, Prepare(context.Background(), cfg, backends))
		require.NoError(mt, mock.ExpectationsWereMet())
	})
}

func TestInitializationPlacementErrors(t *testing.T) {
	cfg := config.DefaultConfig()
	for _, change := range []func(*config.Config){
		func(c *config.Config) { c.Topology.Document.Strategy = "unknown" },
		func(c *config.Config) { c.Topology.Document.Primary = "missing" },
		func(c *config.Config) {
			b := c.Backends["default_mongo"]
			b.Mongo.URI = "mongodb://user:secret@invalid:port"
			c.Backends["default_mongo"] = b
		},
		func(c *config.Config) { c.Topology.Document.Primary = "default_postgres" },
	} {
		current := config.DefaultConfig()
		change(&current)
		_, err := InitializationPlacements(current, &Backends{})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "secret")
		assert.Error(t, Prepare(context.Background(), current, &Backends{}))
	}
	_, err := MongoPlacements(cfg, &Backends{}, "sys", "sys", []string{"default_mongo"}, nil, nil)
	require.Error(t, err)
	failure := errors.New("secret URI")
	err = &PlacementError{Owner: "sys", Cause: failure}
	assert.ErrorIs(t, err, failure)
	assert.NotContains(t, err.Error(), "secret")
}

func TestMongoEndpointIdentityIsPureAndCredentialIndependent(t *testing.T) {
	for _, pair := range [][2]string{
		{"mongodb://u:one@A.example:27017,b.example:27017/db?replicaSet=rs", "mongodb://other:two@b.example:27017,a.example:27017/db?replicaSet=rs"},
		{"mongodb+srv://u:one@unresolved.invalid/db?replicaSet=rs&srvMaxHosts=0", "mongodb+srv://other:two@unresolved.invalid/db?replicaSet=rs&srvServiceName=mongodb"},
	} {
		a, err := mongoEndpointIdentity(pair[0])
		require.NoError(t, err)
		b, err := mongoEndpointIdentity(pair[1])
		require.NoError(t, err)
		assert.Equal(t, a, b)
	}
	a, err := mongoEndpointIdentity("mongodb+srv://unresolved.invalid/db")
	require.NoError(t, err)
	b, err := mongoEndpointIdentity("mongodb+srv://other.invalid/db")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	for _, uri := range []string{"invalid", "mongodb+srv://unresolved.invalid/db?bad=%not-valid", "mongodb://host:port"} {
		_, err := mongoEndpointIdentity(uri)
		assert.Error(t, err)
	}
}
