package core_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	mongostore "github.com/syntrixbase/syntrix/internal/core/storage/mongo"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	queryclient "github.com/syntrixbase/syntrix/internal/query/client"
	"github.com/syntrixbase/syntrix/pkg/model"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

func TestPullSourceMongoMatchingSetAcrossNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017"
	}
	databaseName := fmt.Sprintf("pull_source_%d", time.Now().UnixNano())
	connect := func() *mongo.Client {
		client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetServerSelectionTimeout(3*time.Second).SetWriteConcern(writeconcern.Majority()))
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, client.Disconnect(cleanup))
		})
		require.NoError(t, client.Ping(ctx, nil))
		return client
	}
	firstClient, secondClient := connect(), connect()
	db := firstClient.Database(databaseName)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, db.Drop(cleanup))
	})
	store := mongostore.NewDocumentStore(firstClient, db, "documents", "system", time.Hour)
	require.NoError(t, store.(interface{ EnsureIndexes(context.Context) error }).EnsureIndexes(ctx))
	secondStore := mongostore.NewDocumentStore(secondClient, secondClient.Database(databaseName), "documents", "system", time.Hour)
	nodes := []*queryclient.Client{mongoPullNode(t, store), mongoPullNode(t, secondStore)}
	create := func(id string, active bool) {
		require.NoError(t, store.Create(ctx, "app", types.NewStoredDoc("app", "users", id, map[string]any{"active": active})))
	}
	create("a", true)
	create("b", false)
	create("c", true)
	req := types.ReplicationPullRequest{Collection: "users", DatabaseIdentity: "entity-app", Limit: 1, Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{{Field: "active", Op: model.OpEq, Value: true}}}}
	first, err := nodes[0].Pull(ctx, "app", req)
	require.NoError(t, err)
	require.Equal(t, "scan", first.Phase)
	require.Len(t, first.Events, 1)
	require.Equal(t, "a", first.Events[0].Document.GetID())
	members := map[string]model.Document{"a": first.Events[0].Document}
	req.Checkpoint = first.Checkpoint
	require.NoError(t, store.Update(ctx, "app", "users/a", map[string]any{"active": false}, nil))
	require.NoError(t, store.Update(ctx, "app", "users/b", map[string]any{"active": true}, nil))
	require.NoError(t, store.Delete(ctx, "app", "users/c", nil))
	create("0", true)
	pulls := 1
	bootstrapComplete := false
	var observed []types.ReplicationEvent
	drain := func() {
		for i := 0; i < 64; i++ {
			page, err := nodes[pulls%2].Pull(ctx, "app", req)
			pulls++
			require.NoError(t, err)
			require.Equal(t, first.SourceHash, page.SourceHash)
			require.Equal(t, first.GenerationID, page.GenerationID)
			require.Equal(t, "entity-app", page.DatabaseIdentity)
			if bootstrapComplete {
				require.True(t, page.BootstrapComplete)
			}
			bootstrapComplete = page.BootstrapComplete
			for _, event := range page.Events {
				observed = append(observed, event)
				switch event.Type {
				case types.ReplicationUpsert:
					members[event.Document.GetID()] = event.Document
				case types.ReplicationLeave, types.ReplicationDelete:
					require.Nil(t, event.Document)
					delete(members, event.ID)
				default:
					t.Fatalf("unknown event type %q", event.Type)
				}
			}
			req.Checkpoint = page.Checkpoint
			if page.CaughtUp {
				require.True(t, page.BootstrapComplete)
				require.Equal(t, "live", page.Phase)
				return
			}
		}
		t.Fatal("query source did not reach committed watermark")
	}
	drain()
	require.Len(t, members, 2)
	require.Contains(t, members, "0")
	require.Contains(t, members, "b")
	require.Contains(t, observed, types.ReplicationEvent{Type: types.ReplicationLeave, ID: "a"})
	require.Contains(t, observed, types.ReplicationEvent{Type: types.ReplicationDelete, ID: "c"})
	observed = nil
	require.NoError(t, store.Update(ctx, "app", "users/a", map[string]any{"active": true}, nil))
	require.NoError(t, store.Update(ctx, "app", "users/b", map[string]any{"active": false}, nil))
	drain()
	require.Contains(t, members, "a")
	require.NotContains(t, members, "b")
	require.Contains(t, observed, types.ReplicationEvent{Type: types.ReplicationLeave, ID: "b"})

	observed = nil
	require.NoError(t, store.Delete(ctx, "app", "users/a", nil))
	create("a", true)
	drain()
	require.Equal(t, types.ReplicationEvent{Type: types.ReplicationDelete, ID: "a"}, observed[0])
	require.Equal(t, types.ReplicationUpsert, observed[1].Type)
	require.Equal(t, int64(1), members["a"]["version"])
}
