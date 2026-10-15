package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/codetreker/syntrix/internal/core/storage/config"
	storagemongo "github.com/codetreker/syntrix/internal/core/storage/mongo"
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
	mt.Run("distinct SRV services preserve routed physical placements", func(mt *mtest.T) {
		cfg := config.DefaultConfig()
		backend := cfg.Backends["default_mongo"]
		backend.Mongo.URI = "mongodb+srv://unresolved.invalid/owned?srvServiceName=documents"
		cfg.Backends["default_mongo"] = backend
		backend.Mongo.URI = "mongodb+srv://unresolved.invalid/owned?SRVSERVICENAME=DOCUMENTS"
		cfg.Backends["alias"] = backend
		backend.Mongo.URI = "mongodb+srv://unresolved.invalid/owned?srvServiceName=archive"
		cfg.Backends["archive"] = backend
		cfg.Topology.Document.Strategy = "read_write_split"
		cfg.Topology.Document.Replica = "alias"
		cfg.Databases["archive"] = config.DatabaseConfig{Backend: "archive"}
		provider := &backendTestProvider{client: mt.Client, dbName: "owned"}
		db, mock, err := sqlmock.New()
		require.NoError(mt, err)
		defer db.Close()
		backends := &Backends{providers: map[string]Provider{"default_mongo": provider, "alias": provider, "archive": provider}, postgresDB: db}
		placements, err := InitializationPlacements(cfg, backends)
		require.NoError(mt, err)
		require.Len(mt, placements, 4)
		owners := map[string]int{}
		physical := map[string]int{}
		for _, p := range placements {
			owners[p.Owner]++
			physical[p.Backend]++
			require.Len(mt, p.Required, 1)
			if p.Owner == "documents" {
				require.Len(mt, p.Cleanup, 1)
			} else {
				assert.Empty(mt, p.Cleanup)
			}
			mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 26, Message: "absent collection"}), mtest.CreateSuccessResponse(), mtest.CreateCursorResponse(0, p.Database+"."+p.Collection, mtest.FirstBatch, bson.D{{Key: "name", Value: "database_1_collection_hash_1"}, {Key: "key", Value: bson.D{{Key: "database", Value: 1}, {Key: "collection_hash", Value: 1}}}}))
		}
		assert.Equal(mt, map[string]int{"documents": 2, "sys": 2}, owners)
		require.Len(mt, physical, 2)
		for _, count := range physical {
			assert.Equal(mt, 2, count)
		}
		expectSchemaInitialization(mock, "databases")
		require.NoError(mt, Prepare(context.Background(), cfg, backends))
		require.NoError(mt, mock.ExpectationsWereMet())
		creates := 0
		for _, command := range mt.GetAllStartedEvents() {
			if command.CommandName == "createIndexes" {
				creates++
			}
		}
		assert.Equal(mt, 4, creates)
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

func TestInitializationPlacementsRetainWriteOwner(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, tc := range []struct {
		name, primary, replica, routed string
	}{
		{name: "primary sorts before replica", primary: "a_writer", replica: "z_reader"},
		{name: "primary sorts after replica", primary: "z_writer", replica: "a_reader"},
		{name: "routed writer shares replica endpoint", primary: "primary", replica: "z_reader", routed: "a_routed"},
	} {
		mt.Run(tc.name, func(mt *mtest.T) {
			readonly := mtest.New(mt.T, mtest.NewOptions().ClientType(mtest.Mock))
			readonly.Run("read-only alias", func(readonly *mtest.T) {
				cfg := config.DefaultConfig()
				backend := cfg.Backends["default_mongo"]
				backend.Mongo.URI = "mongodb://writer:write@primary.invalid/owned"
				cfg.Backends[tc.primary] = backend
				backend.Mongo.URI = "mongodb://reader:read@primary.invalid/owned"
				if tc.routed != "" {
					backend.Mongo.URI = "mongodb://reader:read@routed.invalid/owned"
				}
				cfg.Backends[tc.replica] = backend
				cfg.Topology.Document.Primary = tc.primary
				cfg.Topology.Document.Strategy = "read_write_split"
				cfg.Topology.Document.Replica = tc.replica
				writer := &backendTestProvider{client: mt.Client, dbName: "owned"}
				providers := map[string]Provider{tc.primary: writer, tc.replica: &backendTestProvider{client: readonly.Client, dbName: "owned"}}
				if tc.routed != "" {
					backend.Mongo.URI = "mongodb://writer:write@routed.invalid/owned"
					cfg.Backends[tc.routed] = backend
					cfg.Databases["routed"] = config.DatabaseConfig{Backend: tc.routed}
					providers[tc.routed] = writer
				}
				db, mock, err := sqlmock.New()
				require.NoError(readonly, err)
				defer db.Close()
				backends := &Backends{providers: providers, postgresDB: db}
				placements, err := InitializationPlacements(cfg, backends)
				require.NoError(readonly, err)
				expected := 2
				if tc.routed != "" {
					expected = 4
				}
				require.Len(readonly, placements, expected)
				for _, p := range placements {
					assert.True(readonly, p.Handle.Database().Client() == mt.Client, "%s must use a routed write owner", p.Owner)
					addPlacementIndexCreationResponses(mt, p, p.Required)
				}
				readonly.AddMockResponses(
					mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 26, Message: "absent collection"}),
					mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "read-only credentials"}),
				)
				expectSchemaInitialization(mock, "databases")
				require.NoError(readonly, Prepare(context.Background(), cfg, backends))
				require.NoError(readonly, mock.ExpectationsWereMet())
				for _, p := range placements {
					addPlacementIndexCreationResponses(mt, p, p.Cleanup)
					require.NoError(readonly, storagemongo.EnsureIndexDefinitions(context.Background(), p.Handle, p.Cleanup))
				}
				assert.Empty(readonly, readonly.GetAllStartedEvents())
				creates := 0
				for _, command := range mt.GetAllStartedEvents() {
					if command.CommandName == "createIndexes" {
						creates++
					}
				}
				assert.Equal(readonly, expected*3/2, creates)
			})
		})
	}
}

func addPlacementIndexCreationResponses(mt *mtest.T, p MongoPlacement, definitions []storagemongo.IndexDefinition) {
	mt.Helper()
	if len(definitions) == 0 {
		return
	}
	mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 26, Message: "absent collection"}))
	indexes := make([]bson.D, 0, len(definitions))
	for _, d := range definitions {
		mt.AddMockResponses(mtest.CreateSuccessResponse())
		index := bson.D{{Key: "name", Value: d.Name}, {Key: "key", Value: d.Keys}}
		if d.ExpireAfterSeconds != nil {
			index = append(index, bson.E{Key: "expireAfterSeconds", Value: *d.ExpireAfterSeconds})
		}
		indexes = append(indexes, index)
	}
	mt.AddMockResponses(mtest.CreateCursorResponse(0, p.Database+"."+p.Collection, mtest.FirstBatch, indexes...))
}

func TestMongoEndpointIdentityIsPureAndCredentialIndependent(t *testing.T) {
	for _, pair := range [][2]string{
		{"mongodb://u:one@A.example:27017,b.example:27017/db?replicaSet=rs", "mongodb://other:two@b.example:27017,a.example:27017/db?replicaSet=rs"},
		{"mongodb+srv://u:one@unresolved.invalid/db?replicaSet=rs&srvMaxHosts=0", "mongodb+srv://other:two@unresolved.invalid/db?replicaSet=rs&srvServiceName=mongodb"},
		{"mongodb+srv://unresolved.invalid/db", "mongodb+srv://UNRESOLVED.invalid/db?SRVSERVICENAME=MONGODB"},
		{"mongodb+srv://unresolved.invalid/db?srvServiceName=documents", "mongodb+srv://unresolved.invalid/db?sRvSeRvIcEnAmE=%44OCUMENTS"},
		{"mongodb+srv://unresolved.invalid/db?srvServiceName=documents", "mongodb+srv://unresolved.invalid/db?srvServiceName=archive;SRVSERVICENAME=DOCUMENTS"},
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
	b, err = mongoEndpointIdentity("mongodb+srv://unresolved.invalid/db?srvServiceName=documents")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	a, err = mongoEndpointIdentity("mongodb+srv://unresolved.invalid/db?srvServiceName=archive")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	for _, uri := range []string{"invalid", "mongodb+srv://unresolved.invalid/db?bad=%not-valid", "mongodb+srv://unresolved.invalid/db?bad%not-valid=value", "mongodb+srv://unresolved.invalid/db?bad", "mongodb+srv://unresolved.invalid/db?srvServiceName=", "mongodb+srv://unresolved.invalid/db?srvServiceName=" + strings.Repeat("a", 63), "mongodb://host:port"} {
		_, err := mongoEndpointIdentity(uri)
		assert.Error(t, err)
	}
}
