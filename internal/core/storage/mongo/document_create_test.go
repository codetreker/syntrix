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

func TestDocumentCreateConditionsBeforeStorage(t *testing.T) {
	store := &documentStore{}
	version := int64(-1)
	for _, opts := range [][]types.CreateOptions{{{}, {}}, {{Condition: "unknown"}}, {{ExpectedVersion: &version}}, {{Condition: types.CreateIfAbsent, ExpectedVersion: &version}}, {{Condition: types.CreateIfTombstone}}, {{Condition: types.CreateIfTombstone, ExpectedVersion: &version}}} {
		require.Error(t, store.Create(context.Background(), "app", types.StoredDoc{}, opts...))
	}
}

func TestDocumentCreateConditionalCommands(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	for _, condition := range []types.CreateCondition{types.CreateIfAbsent, types.CreateIfTombstone} {
		for _, outcome := range []string{"success", "conflict", "error"} {
			mt.Run(string(condition)+"/"+outcome, func(mt *mtest.T) {
				version := int64(0)
				opts := types.CreateOptions{Condition: condition}
				commandName := "insert"
				if condition == types.CreateIfTombstone {
					opts.ExpectedVersion = &version
					commandName = "update"
				}
				response := mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1})
				if outcome == "conflict" {
					if condition == types.CreateIfAbsent {
						response = mtest.CreateWriteErrorsResponse(mtest.WriteError{Index: 0, Code: 11000, Message: "duplicate"})
					} else {
						response = mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 0}, bson.E{Key: "nModified", Value: 0})
					}
				} else if outcome == "error" {
					response = mtest.CreateCommandErrorResponse(mtest.CommandError{Code: 13, Message: "denied"})
				}
				mt.AddMockResponses(response)
				store := NewDocumentStore(mt.Client, mt.DB, mt.Coll.Name(), "sys", 0)
				doc := types.NewStoredDoc("app", "users", "alice", nil)
				err := store.Create(context.Background(), "app", doc, opts)
				switch outcome {
				case "success":
					require.NoError(mt, err)
				case "conflict":
					expected := model.ErrExists
					if condition == types.CreateIfTombstone {
						expected = model.ErrPreconditionFailed
					}
					require.ErrorIs(mt, err, expected)
				case "error":
					var commandErr mongo.CommandError
					require.ErrorAs(mt, err, &commandErr)
					assert.Equal(mt, int32(13), commandErr.Code)
				}
				commands := mt.GetAllStartedEvents()
				require.Len(mt, commands, 1, "conditional creation performs a single atomic write without fallback")
				assert.Equal(mt, commandName, commands[0].CommandName)
				if condition == types.CreateIfTombstone {
					update := commands[0].Command.Lookup("updates").Array().Index(0).Value().Document()
					filter := update.Lookup("q").Document()
					assert.Equal(mt, doc.Id, filter.Lookup("_id").StringValue())
					assert.Equal(mt, "app", filter.Lookup("database").StringValue())
					assert.True(mt, filter.Lookup("deleted").Boolean())
					assert.Equal(mt, version, filter.Lookup("version").Int64())
					upsert := update.Lookup("upsert")
					assert.True(mt, upsert.Type == 0 || !upsert.Boolean())
				}
			})
		}
	}
}

func TestDocumentCreateConditionalState(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	for _, state := range []string{"missing", "live", "newer tombstone", "matching tombstone"} {
		t.Run(state, func(t *testing.T) {
			doc := types.NewStoredDoc("app", "users", state, map[string]interface{}{"name": "original"})
			doc.Deleted = state != "live"
			doc.Version = 7
			if state == "newer tombstone" {
				doc.Version++
			}
			if state != "missing" {
				_, err := env.DB.Collection("docs").InsertOne(ctx, doc)
				require.NoError(t, err)
				absent := types.NewStoredDoc("app", "users", state, map[string]interface{}{"name": "absent"})
				require.ErrorIs(t, store.Create(ctx, "app", absent, types.CreateOptions{Condition: types.CreateIfAbsent}), model.ErrExists)
			}
			version := int64(7)
			replacement := types.NewStoredDoc("app", "users", state, map[string]interface{}{"name": "replacement"})
			err := store.Create(ctx, "app", replacement, types.CreateOptions{Condition: types.CreateIfTombstone, ExpectedVersion: &version})
			if state == "matching tombstone" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, model.ErrPreconditionFailed)
			}
			current, err := store.Get(ctx, "app", doc.Fullpath, types.ReadOptions{ShowDeleted: true})
			switch state {
			case "missing":
				require.ErrorIs(t, err, model.ErrNotFound)
			case "matching tombstone":
				require.NoError(t, err)
				assert.Equal(t, int64(1), current.Version)
				assert.False(t, current.Deleted)
				assert.Equal(t, "replacement", current.Data["name"])
			default:
				require.NoError(t, err)
				assert.Equal(t, doc.Version, current.Version)
				assert.Equal(t, doc.Deleted, current.Deleted)
				assert.Equal(t, "original", current.Data["name"])
			}
		})
	}
}

func TestDocumentCreateConditionalRaces(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	for _, condition := range []types.CreateCondition{types.CreateIfAbsent, types.CreateIfTombstone} {
		t.Run(string(condition), func(t *testing.T) {
			version := int64(7)
			opts := types.CreateOptions{Condition: condition}
			expectedErr := model.ErrExists
			if condition == types.CreateIfTombstone {
				opts.ExpectedVersion = &version
				expectedErr = model.ErrPreconditionFailed
				doc := types.NewStoredDoc("app", "users", string(condition), nil)
				doc.Version, doc.Deleted = version, true
				_, err := env.DB.Collection("docs").InsertOne(ctx, doc)
				require.NoError(t, err)
			}
			start := make(chan struct{})
			type outcome struct {
				name string
				err  error
			}
			results := make(chan outcome, 2)
			for _, name := range []string{"first", "second"} {
				go func() {
					<-start
					doc := types.NewStoredDoc("app", "users", string(condition), map[string]interface{}{"name": name})
					results <- outcome{name, store.Create(ctx, "app", doc, opts)}
				}()
			}
			close(start)
			winner := ""
			for range 2 {
				select {
				case result := <-results:
					if result.err == nil {
						require.Empty(t, winner)
						winner = result.name
					} else {
						require.ErrorIs(t, result.err, expectedErr)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			require.NotEmpty(t, winner)
			current, err := store.Get(ctx, "app", "users/"+string(condition))
			require.NoError(t, err)
			assert.Equal(t, winner, current.Data["name"])
			assert.Equal(t, int64(1), current.Version)
		})
	}
}

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
