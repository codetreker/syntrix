package mongo

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/event"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestDocumentCreateDuplicateOutcomes(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, scenario := range []string{"live", "missing", "read error", "invalid document", "replacement error", "recreated concurrently", "recreated", "insert error"} {
		mt.Run(scenario, func(mt *mtest.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			doc := types.NewStoredDoc("app", "users", "alice", map[string]interface{}{"name": "new"})
			doc.CollectionHash = ""
			store := NewDocumentStore(mt.Client, mt.DB, mt.Coll.Name(), "sys", 0)
			failure := mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "denied"})
			if scenario == "insert error" {
				mt.AddMockResponses(failure)
			} else {
				mt.AddMockResponses(mtest.CreateWriteErrorsResponse(mtest.WriteError{Index: 0, Code: 11000, Message: "duplicate"}))
				existing := bson.D{{Key: "_id", Value: doc.Id}, {Key: "database", Value: "app"}, {Key: "deleted", Value: scenario != "live"}}
				switch scenario {
				case "missing":
					mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+"."+mt.Coll.Name(), mtest.FirstBatch))
				case "read error":
					mt.AddMockResponses(failure)
				case "invalid document":
					mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+"."+mt.Coll.Name(), mtest.FirstBatch, bson.D{{Key: "deleted", Value: "invalid"}}))
				default:
					mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+"."+mt.Coll.Name(), mtest.FirstBatch, existing))
				}
				switch scenario {
				case "replacement error":
					mt.AddMockResponses(failure)
				case "recreated concurrently":
					mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 0}, bson.E{Key: "nModified", Value: 0}))
				case "recreated":
					mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}))
				}
			}
			err := store.Create(ctx, "app", doc)
			switch scenario {
			case "live", "missing", "recreated concurrently":
				require.ErrorIs(mt, err, model.ErrExists)
			case "recreated":
				require.NoError(mt, err)
			case "invalid document":
				require.Error(mt, err)
				assert.NotErrorIs(mt, err, model.ErrExists)
			default:
				var commandErr mongo.CommandError
				require.ErrorAs(mt, err, &commandErr)
				assert.Equal(mt, int32(13), commandErr.Code)
			}
			for _, command := range mt.GetAllStartedEvents() {
				if command.CommandName != "update" {
					continue
				}
				filter := command.Command.Lookup("updates").Array().Index(0).Value().Document().Lookup("q").Document()
				assert.Equal(mt, doc.Id, filter.Lookup("_id").StringValue())
				assert.Equal(mt, "app", filter.Lookup("database").StringValue())
				assert.Equal(mt, bson.TypeBoolean, filter.Lookup("deleted").Type)
				assert.True(mt, filter.Lookup("deleted").Boolean())
			}
		})
	}
}

func TestDocumentCreateConcurrentTombstoneReplacement(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	doc := types.NewStoredDoc("app", "users", "alice", map[string]interface{}{"name": "old"})
	require.NoError(t, store.Create(ctx, "app", doc))
	require.NoError(t, store.Delete(ctx, "app", doc.Fullpath, nil))

	var reads atomic.Int32
	bothRead := make(chan struct{})
	monitor := &event.CommandMonitor{Succeeded: func(_ context.Context, command *event.CommandSucceededEvent) {
		if command.CommandName != "find" {
			return
		}
		// Both creators must receive the same tombstone before either replaces it.
		if reads.Add(1) == 2 {
			close(bothRead)
		}
		select {
		case <-bothRead:
		case <-ctx.Done():
		}
	}}
	type outcome struct {
		name string
		err  error
	}
	results := make(chan outcome, 2)
	for _, name := range []string{"first", "second"} {
		client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI).SetMonitor(monitor))
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			require.NoError(t, client.Disconnect(cleanupCtx))
		})
		creator := NewDocumentStore(client, client.Database(env.DBName), "docs", "sys", 0)
		go func(name string) {
			replacement := types.NewStoredDoc("app", "users", "alice", map[string]interface{}{"name": name})
			results <- outcome{name, creator.Create(ctx, "app", replacement)}
		}(name)
	}
	winner := ""
	for range 2 {
		select {
		case result := <-results:
			if result.err == nil {
				require.Empty(t, winner, "only one creator may replace a tombstone")
				winner = result.name
			} else {
				require.ErrorIs(t, result.err, model.ErrExists)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	require.NotEmpty(t, winner)
	assert.Equal(t, int32(2), reads.Load())
	current, err := store.Get(ctx, "app", doc.Fullpath)
	require.NoError(t, err)
	assert.Equal(t, winner, current.Data["name"])
	assert.False(t, current.Deleted)
	assert.Equal(t, int64(1), current.Version)
}
