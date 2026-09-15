package mongo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func nextWatchEvent(t *testing.T, stream types.WatchStream) types.WatchFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for {
		frame, err := stream.Next(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, frame.Checkpoint)
		if frame.Event != nil {
			return frame
		}
	}
}

func requireWatchCode(t *testing.T, err error, code types.WatchErrorCode) {
	t.Helper()
	var watchErr *types.WatchError
	require.ErrorAs(t, err, &watchErr)
	assert.Equal(t, code, watchErr.Code)
}

func TestMongoWatchResumeAcrossClients(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	first, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
	require.NoError(t, err)
	initial := first.InitialCheckpoint()
	require.NotEmpty(t, initial)
	require.NoError(t, first.Close())
	doc := types.NewStoredDoc("tenant", "users", "alice", map[string]interface{}{"v": 1})
	require.NoError(t, store.Create(ctx, "tenant", doc))

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Disconnect(context.Background())) })
	resumedStore := NewDocumentStore(client, client.Database(env.DBName), "docs", "sys", 0)
	serialized, err := json.Marshal(initial)
	require.NoError(t, err)
	var persisted types.WatchCheckpoint
	require.NoError(t, json.Unmarshal(serialized, &persisted))
	stream, err := resumedStore.Watch(ctx, "tenant", "users", persisted, types.WatchOptions{})
	require.NoError(t, err)
	require.Equal(t, persisted, stream.InitialCheckpoint())
	created := nextWatchEvent(t, stream)
	require.Equal(t, types.EventCreate, created.Event.Type)
	require.Equal(t, doc.Id, created.Event.Id)
	require.NoError(t, stream.Close())

	require.NoError(t, store.Update(ctx, "tenant", "users/alice", map[string]interface{}{"v": 2}, nil))
	require.NoError(t, store.Update(ctx, "tenant", "users/alice", map[string]interface{}{"v": 3}, nil))
	stream, err = resumedStore.Watch(ctx, "tenant", "users", created.Checkpoint, types.WatchOptions{})
	require.NoError(t, err)
	a, b := nextWatchEvent(t, stream), nextWatchEvent(t, stream)
	require.Equal(t, types.EventUpdate, a.Event.Type)
	require.Equal(t, types.EventUpdate, b.Event.Type)
	require.Equal(t, a.Event.Id, b.Event.Id)
	require.NotEqual(t, a.Event.ChangeID, b.Event.ChangeID)
	require.NotEqual(t, a.Checkpoint, b.Checkpoint)
	require.Equal(t, "alice", created.Event.Document.Data["id"])
	require.NoError(t, stream.Close())

	replay, err := store.Watch(ctx, "tenant", "users", created.Checkpoint, types.WatchOptions{})
	require.NoError(t, err)
	defer replay.Close()
	replayed := nextWatchEvent(t, replay)
	require.Equal(t, a.Event.ChangeID, replayed.Event.ChangeID)
	require.Equal(t, a.Checkpoint, replayed.Checkpoint)
	require.Equal(t, b.Event.ChangeID, nextWatchEvent(t, replay).Event.ChangeID)
	idle, err := replay.Next(ctx)
	require.NoError(t, err)
	require.Nil(t, idle.Event)
	require.NotEmpty(t, idle.Checkpoint)
	require.NoError(t, replay.Close())
	require.NoError(t, store.Delete(ctx, "tenant", "users/alice", nil))
	require.NoError(t, store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "alice", map[string]interface{}{"v": 4})))
	replay, err = store.Watch(ctx, "tenant", "users", idle.Checkpoint, types.WatchOptions{})
	require.NoError(t, err)
	defer replay.Close()
	require.Equal(t, types.EventDelete, nextWatchEvent(t, replay).Event.Type)
	require.Equal(t, types.EventCreate, nextWatchEvent(t, replay).Event.Type)
}

func TestMongoWatchScopeAndSource(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	_, err := store.Watch(ctx, "", "users", "", types.WatchOptions{})
	requireWatchCode(t, err, types.WatchInvalidScope)
	stream, err := store.Watch(ctx, "tenant.*", "users", "", types.WatchOptions{})
	require.NoError(t, err)
	defer stream.Close()
	checkpoint := stream.InitialCheckpoint()
	for _, tc := range []struct {
		database, collection string
		options              types.WatchOptions
	}{
		{"other", "users", types.WatchOptions{}}, {"tenant.*", "other", types.WatchOptions{}}, {"tenant.*", "users", types.WatchOptions{IncludeBefore: true}},
	} {
		_, err := store.Watch(ctx, tc.database, tc.collection, checkpoint, tc.options)
		requireWatchCode(t, err, types.WatchScopeMismatch)
	}
	other := NewDocumentStore(env.Client, env.DB, "other", "sys", 0)
	require.NoError(t, env.DB.CreateCollection(ctx, "other"))
	_, err = other.Watch(ctx, "tenant.*", "users", checkpoint, types.WatchOptions{})
	requireWatchCode(t, err, types.WatchSourceMismatch)
	require.NoError(t, store.Create(ctx, "tenantX", types.NewStoredDoc("tenantX", "users", "foreign", nil)))
	require.NoError(t, store.Create(ctx, "tenant.*", types.NewStoredDoc("tenant.*", "other", "foreign", nil)))
	require.NoError(t, store.Create(ctx, "tenant.*", types.NewStoredDoc("tenant.*", "users", "selected", nil)))
	first, err := stream.Next(ctx)
	require.NoError(t, err)
	require.Nil(t, first.Event)
	require.NotEmpty(t, first.Checkpoint)
	require.Equal(t, types.CalculateDatabase("tenant.*", "users/selected"), nextWatchEvent(t, stream).Event.Id)
	require.NoError(t, env.DB.Collection("docs").Drop(ctx))
	_, err = stream.Next(ctx)
	requireWatchCode(t, err, types.WatchSourceMismatch)
	_, err = stream.Next(ctx)
	requireWatchCode(t, err, types.WatchSourceMismatch)
	_, err = store.Watch(ctx, "tenant.*", "users", checkpoint, types.WatchOptions{})
	requireWatchCode(t, err, types.WatchSourceMismatch)
	require.NoError(t, env.DB.CreateCollection(ctx, "docs"))
	_, err = store.Watch(ctx, "tenant.*", "users", checkpoint, types.WatchOptions{})
	requireWatchCode(t, err, types.WatchSourceMismatch)
}

func TestMongoWatchPhysicalDeleteRouting(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, env.DB.CreateCollection(ctx, "docs", options.CreateCollection().SetChangeStreamPreAndPostImages(bson.M{"enabled": true})))
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	selected := types.NewStoredDoc("tenant", "users", "alice", nil)
	foreign := types.NewStoredDoc("tenant", "other", "bob", nil)
	require.NoError(t, store.Create(ctx, "tenant", selected))
	require.NoError(t, store.Create(ctx, "tenant", foreign))
	stream, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
	require.NoError(t, err)
	defer stream.Close()
	_, err = env.DB.Collection("docs").DeleteOne(ctx, bson.M{"_id": foreign.Id})
	require.NoError(t, err)
	_, err = env.DB.Collection("docs").DeleteOne(ctx, bson.M{"_id": selected.Id})
	require.NoError(t, err)
	for changes := 0; changes < 2; {
		progress, err := stream.Next(ctx)
		require.NoError(t, err)
		require.Nil(t, progress.Event)
		if progress.SourceBytes > 0 {
			require.False(t, progress.CaughtUp)
			changes++
		}
	}

	require.NoError(t, store.Create(ctx, "tenant", selected))
	before, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{IncludeBefore: true})
	require.NoError(t, err)
	defer before.Close()
	require.NoError(t, store.Update(ctx, "tenant", "users/alice", map[string]interface{}{"v": 2}, nil))
	updated := nextWatchEvent(t, before).Event
	require.NotNil(t, updated.Before)
	require.Equal(t, selected.Id, updated.Before.Id)
}

func TestMongoWatchMissingDeleteMetadata(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	doc := types.NewStoredDoc("tenant", "other", "bob", nil)
	require.NoError(t, store.Create(ctx, "tenant", doc))
	scoped, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
	require.NoError(t, err)
	defer scoped.Close()
	all, err := store.Watch(ctx, "tenant", "", "", types.WatchOptions{})
	require.NoError(t, err)
	defer all.Close()
	_, err = env.DB.Collection("docs").DeleteOne(ctx, bson.M{"_id": doc.Id})
	require.NoError(t, err)
	for _, stream := range []types.WatchStream{scoped, all} {
		initial := stream.InitialCheckpoint()
		for {
			frame, err := stream.Next(ctx)
			require.NoError(t, err)
			require.Nil(t, frame.Event)
			if frame.CaughtUp && frame.Checkpoint != initial {
				break
			}
		}
	}
}

func TestMongoWatchCloseAndCancellation(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	stream, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
	require.NoError(t, err)
	defer stream.Close()
	readCtx, cancelRead := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		// Progress frames may be returned before cancellation is observed.
		for {
			if _, err := stream.Next(readCtx); err != nil {
				done <- err
				return
			}
		}
	}()
	cancelRead()
	require.ErrorIs(t, <-done, context.Canceled)
	_, err = stream.Next(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, stream.Close())
	require.NoError(t, stream.Close())
	sibling, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
	require.NoError(t, err)
	defer sibling.Close()
	require.NoError(t, store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "alive", nil)))
	require.Equal(t, types.EventCreate, nextWatchEvent(t, sibling).Event.Type)
}

func TestMongoWatchNestedDatabaseAndSystemIsolation(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	ordinary, err := store.Watch(ctx, "tenant", "", "", types.WatchOptions{})
	require.NoError(t, err)
	defer ordinary.Close()
	nested, err := store.Watch(ctx, "tenant:nested", "users", "", types.WatchOptions{})
	require.NoError(t, err)
	defer nested.Close()
	system, err := store.Watch(ctx, "tenant", "sys/config", "", types.WatchOptions{})
	require.NoError(t, err)
	defer system.Close()
	for _, doc := range []types.StoredDoc{
		types.NewStoredDoc("tenant:nested", "users", "nested", nil),
		types.NewStoredDoc("tenant", "sys/config", "system", nil),
		types.NewStoredDoc("tenant", "users", "ordinary", nil),
	} {
		require.NoError(t, store.Create(ctx, doc.Database, doc))
	}
	require.Equal(t, types.CalculateDatabase("tenant", "users/ordinary"), nextWatchEvent(t, ordinary).Event.Id)
	require.Equal(t, types.CalculateDatabase("tenant:nested", "users/nested"), nextWatchEvent(t, nested).Event.Id)
	require.Equal(t, types.CalculateDatabase("tenant", "sys/config/system"), nextWatchEvent(t, system).Event.Id)
	for _, stream := range []types.WatchStream{ordinary, nested, system} {
		read, stop := context.WithTimeout(ctx, 150*time.Millisecond)
		for {
			frame, err := stream.Next(read)
			if err != nil {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				break
			}
			require.Nil(t, frame.Event, "an unrelated database or source must not produce an event")
		}
		stop()
	}
}

func TestMongoWatchMissingUpdateLookup(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	doc := types.NewStoredDoc("tenant", "users", "alice", nil)
	require.NoError(t, store.Create(ctx, "tenant", doc))
	stream, err := store.Watch(ctx, "tenant", "", "", types.WatchOptions{})
	require.NoError(t, err)
	defer stream.Close()
	require.NoError(t, store.Update(ctx, "tenant", "users/alice", map[string]interface{}{"v": 2}, nil))
	_, err = env.DB.Collection("docs").DeleteOne(ctx, bson.M{"_id": doc.Id})
	require.NoError(t, err)
	for {
		frame, err := stream.Next(ctx)
		if err != nil {
			requireWatchCode(t, err, types.WatchPayloadUnavailable)
			require.Empty(t, frame.Checkpoint)
			break
		}
		require.Nil(t, frame.Event)
	}
}

func TestMongoWatchBeforeIsBestEffort(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	require.NoError(t, store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "alice", nil)))
	stream, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{IncludeBefore: true})
	require.NoError(t, err)
	defer stream.Close()
	require.NoError(t, store.Update(ctx, "tenant", "users/alice", map[string]interface{}{"v": 2}, nil))
	event := nextWatchEvent(t, stream).Event
	require.Equal(t, types.EventUpdate, event.Type)
	require.Nil(t, event.Before)
}

func TestMongoWatchNativeSourceFailures(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0).(*documentStore)
	stopped, stop := context.WithCancel(ctx)
	stop()
	_, err := store.Watch(stopped, "tenant", "users", "", types.WatchOptions{})
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, env.DB.CreateCollection(ctx, "base"))
	require.NoError(t, env.DB.CreateView(ctx, "docs", "base", mongo.Pipeline{}))
	_, err = store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
	requireWatchCode(t, err, types.WatchUnsupported)
	store.dataCollection = ""
	_, err = store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
	require.Error(t, err)
}
