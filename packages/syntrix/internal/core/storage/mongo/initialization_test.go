package mongo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestRequiredIndexDefinitionsRejectPolicyConflicts(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	required, cleanup := DocumentIndexDefinitions()
	for _, test := range []struct {
		name string
		keys bson.D
		opts *options.IndexOptions
	}{
		{"alternate name", required[0].Keys, options.Index().SetName("foreign")},
		{"wrong uniqueness", required[0].Keys, options.Index().SetName(required[0].Name).SetUnique(true)},
		{"reversed keys", bson.D{{Key: "collection_hash", Value: 1}, {Key: "database", Value: 1}}, options.Index().SetName(required[0].Name)},
		{"wrong keys", bson.D{{Key: "other", Value: 1}}, options.Index().SetName(required[0].Name)},
		{"sparse", required[0].Keys, options.Index().SetName(required[0].Name).SetSparse(true)},
		{"hidden", required[0].Keys, options.Index().SetName(required[0].Name).SetHidden(true)},
		{"partial", required[0].Keys, options.Index().SetName(required[0].Name).SetPartialFilterExpression(bson.M{"database": bson.M{"$exists": true}})},
		{"collation", required[0].Keys, options.Index().SetName(required[0].Name).SetCollation(&options.Collation{Locale: "en"})},
	} {
		t.Run(test.name, func(t *testing.T) {
			coll := env.DB.Collection(test.name)
			_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: test.keys, Options: test.opts})
			require.NoError(t, err)
			observed, err := InspectIndexDefinitions(ctx, coll, required)
			require.NoError(t, err)
			require.Len(t, observed, 1)
			assert.Equal(t, "conflict", observed[0].State)
			err = EnsureIndexDefinitions(ctx, coll, required)
			require.ErrorIs(t, err, ErrIndexConflict)
			assert.NotContains(t, err.Error(), "mongodb://")
		})
	}
	coll := env.DB.Collection("ttl")
	_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: cleanup[0].Keys, Options: options.Index().SetName(cleanup[0].Name).SetExpireAfterSeconds(60)})
	require.NoError(t, err)
	observed, err := InspectIndexDefinitions(ctx, coll, cleanup)
	require.NoError(t, err)
	assert.Equal(t, "conflict", observed[0].State)
	_, err = coll.Indexes().DropOne(ctx, cleanup[0].Name)
	require.NoError(t, err)
	require.NoError(t, EnsureIndexDefinitions(ctx, coll, append(required, cleanup...)))
	observed, err = InspectIndexDefinitions(ctx, coll, cleanup)
	require.NoError(t, err)
	assert.Equal(t, "present", observed[0].State)
	withoutTTL := cleanup
	withoutTTL[0].ExpireAfterSeconds = nil
	observed, err = InspectIndexDefinitions(ctx, coll, withoutTTL)
	require.NoError(t, err)
	assert.Equal(t, "conflict", observed[0].State)
	assert.NoError(t, EnsureIndexDefinitions(ctx, nil, nil))
}

func TestIndexOperationFailureDoesNotClaimCompletion(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, stage := range []string{"inspect", "decode", "create", "verify read", "verify conflict"} {
		mt.Run(stage, func(mt *mtest.T) {
			required, _ := DocumentIndexDefinitions()
			switch stage {
			case "inspect":
				mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "secret denied"}))
			case "decode":
				mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+"."+mt.Coll.Name(), mtest.FirstBatch, bson.D{{Key: "name", Value: required[0].Name}, {Key: "key", Value: "invalid"}}))
			case "create":
				mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 26, Message: "absent"}), mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "denied"}))
			case "verify read":
				mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 26, Message: "absent"}), mtest.CreateSuccessResponse(), mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "denied"}))
			case "verify conflict":
				mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 26, Message: "absent"}), mtest.CreateSuccessResponse(), mtest.CreateCursorResponse(0, mt.DB.Name()+"."+mt.Coll.Name(), mtest.FirstBatch))
			}
			err := EnsureIndexDefinitions(context.Background(), mt.Coll, required)
			require.Error(mt, err)
			assert.NotContains(mt, err.Error(), "secret")
		})
	}
	failure := errors.New("secret")
	err := &IndexError{Operation: "create", Index: "required", Cause: failure}
	assert.ErrorIs(t, err, failure)
	assert.NotContains(t, err.Error(), "secret")
	for _, pair := range [][2]any{{int32(1), int64(1)}, {int(1), float64(1)}, {"hashed", "hashed"}} {
		assert.True(t, equalIndexValue(pair[0], pair[1]))
	}
	assert.False(t, equalIndexValue(float64(1.5), int(1)))
	assert.False(t, equalIndexValue("text", int(1)))
}

func TestDocumentIndexPreparationFailureRemainsRetryable(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("source preparation failure prevents cleanup and can retry", func(mt *mtest.T) {
		store := NewDocumentStore(mt.Client, mt.DB, "documents", "sys", time.Hour).(*documentStore)
		mt.AddMockResponses(mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "index permission denied"}))
		err := store.EnsureIndexes(context.Background())
		var cause mongo.CommandError
		require.ErrorAs(mt, err, &cause)
		assert.Equal(mt, int32(13), cause.Code)
		commands := mt.GetAllStartedEvents()
		require.Len(mt, commands, 1)
		assert.Contains(mt, commands[0].Command.String(), sourceScanIndexName)
		assert.NotContains(mt, commands[0].Command.String(), "expireAfterSeconds")
		mt.AddMockResponses(
			mtest.CreateSuccessResponse(), mtest.CreateSuccessResponse(),
			mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 26, Message: "absent"}),
			mtest.CreateSuccessResponse(), mtest.CreateSuccessResponse(),
			mtest.CreateCursorResponse(0, mt.DB.Name()+".documents", mtest.FirstBatch,
				bson.D{{Key: "name", Value: "database_1_collection_hash_1"}, {Key: "key", Value: bson.D{{Key: "database", Value: 1}, {Key: "collection_hash", Value: 1}}}},
				bson.D{{Key: "name", Value: "sys_expires_at_1"}, {Key: "key", Value: bson.D{{Key: "sys_expires_at", Value: 1}}}, {Key: "expireAfterSeconds", Value: 0}},
			),
		)
		require.NoError(mt, store.EnsureIndexes(context.Background()))
	})
}
