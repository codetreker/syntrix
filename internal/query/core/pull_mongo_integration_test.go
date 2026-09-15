package core_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	mongostore "github.com/syntrixbase/syntrix/internal/core/storage/mongo"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	queryclient "github.com/syntrixbase/syntrix/internal/query/client"
	"github.com/syntrixbase/syntrix/internal/query/core"
	querygrpc "github.com/syntrixbase/syntrix/internal/query/grpc"
	"github.com/syntrixbase/syntrix/pkg/model"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type watchOnlyPullStore struct {
	types.DocumentStore
	types.DocumentScanner
}

func (s *watchOnlyPullStore) Get(context.Context, string, string, ...types.ReadOptions) (*types.StoredDoc, error) {
	return nil, fmt.Errorf("Pull must use the document carried by Watch")
}

func (s *watchOnlyPullStore) GetMany(context.Context, string, []string, ...types.ReadOptions) ([]*types.StoredDoc, error) {
	return nil, fmt.Errorf("Pull must not materialize Watch events through GetMany")
}

func mongoPullNode(t *testing.T, store types.DocumentStore) *queryclient.Client {
	t.Helper()
	source := &watchOnlyPullStore{DocumentStore: store, DocumentScanner: store.(types.DocumentScanner)}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterQueryServiceServer(server, querygrpc.NewServer(core.New(source, nil)))
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, listener.Close())
		select {
		case err := <-finished:
			if err != nil {
				require.ErrorIs(t, err, grpc.ErrServerStopped)
			}
		case <-time.After(5 * time.Second):
			t.Error("Pull integration Query node did not stop")
		}
	})
	client, err := queryclient.NewWithOptions("passthrough:///pull-mongo", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func TestPullMongoBootstrapReplayAcrossNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017"
	}
	databaseName := fmt.Sprintf("pull_watch_%d", time.Now().UnixNano())
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
	firstClient := connect()
	db := firstClient.Database(databaseName)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, db.Drop(cleanup))
	})
	store := mongostore.NewDocumentStore(firstClient, db, "documents", "system", time.Hour)
	require.NoError(t, store.(interface{ EnsureIndexes(context.Context) error }).EnsureIndexes(ctx))
	create := func(id string, value int64) {
		require.NoError(t, store.Create(ctx, "app", types.NewStoredDoc("app", "users", id, map[string]any{"value": value})))
	}
	for _, id := range []string{"a", "c", "z"} {
		create(id, 1)
	}
	firstNode := mongoPullNode(t, store)
	request := types.ReplicationPullRequest{Collection: "users", DatabaseIdentity: "entity-app", Limit: 1}
	first, err := firstNode.Pull(ctx, "app", request)
	require.NoError(t, err)
	require.Len(t, first.Documents, 1)
	require.Equal(t, "a", first.Documents[0].GetID())
	require.False(t, first.CaughtUp)
	request.Checkpoint = first.Checkpoint
	mirror := map[string]model.Document{"a": first.Documents[0]}
	apply := func(page *types.ReplicationPullResponse) {
		for _, doc := range page.Documents {
			if deleted, _ := doc["deleted"].(bool); deleted {
				delete(mirror, doc.GetID())
			} else {
				mirror[doc.GetID()] = doc
			}
		}
		request.Checkpoint = page.Checkpoint
	}
	// The ID scan has passed a and 0. Only replay from its original boundary can
	// discover these later commits; the remaining scan independently sees b and c.
	require.NoError(t, store.Update(ctx, "app", "users/a", map[string]any{"value": int64(2)}, nil))
	create("0", 3)
	create("b", 4)
	require.NoError(t, store.Delete(ctx, "app", "users/c", nil))
	secondClient := connect()
	secondStore := mongostore.NewDocumentStore(secondClient, secondClient.Database(databaseName), "documents", "system", time.Hour)
	secondNode := mongoPullNode(t, secondStore)
	nodes := []*queryclient.Client{secondNode, firstNode}
	pulls := 0
	var emitted []model.Document
	drain := func() {
		for i := 0; i < 64; i++ {
			page, err := nodes[pulls%len(nodes)].Pull(ctx, "app", request)
			pulls++
			require.NoError(t, err)
			require.NotEmpty(t, page.Checkpoint)
			emitted = append(emitted, page.Documents...)
			apply(page)
			if page.CaughtUp {
				return
			}
		}
		t.Fatal("Pull did not reach a source watermark within bounded pages")
	}
	drain()
	require.Len(t, mirror, 4)
	for id, value := range map[string]int64{"0": 3, "a": 2, "b": 4, "z": 1} {
		require.Equal(t, value, mirror[id]["value"], "document %s", id)
	}
	require.NotContains(t, mirror, "c")

	// Recreate resets the document version. Both operations must survive the
	// public checkpoint and cross-node page boundary in source order.
	require.Greater(t, mirror["a"]["version"].(int64), int64(1))
	require.NoError(t, store.Delete(ctx, "app", "users/a", nil))
	create("a", 5)
	emitted = nil
	drain()
	require.Len(t, emitted, 2)
	require.Equal(t, model.Document{"id": "a", "collection": "users", "deleted": true}, emitted[0])
	require.Equal(t, "a", emitted[1].GetID())
	require.Equal(t, int64(1), mirror["a"]["version"])
	require.Equal(t, int64(5), mirror["a"]["value"])

	// A single transaction produces distinct native event positions despite one
	// commit timestamp and equal application timestamps on all three documents.
	session, err := firstClient.StartSession()
	require.NoError(t, err)
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(transaction mongo.SessionContext) (any, error) {
		docs := make([]any, 0, 3)
		for _, id := range []string{"txn-a", "txn-b", "txn-c"} {
			doc := types.NewStoredDoc("app", "users", id, map[string]any{"value": int64(17)})
			doc.UpdatedAt = 17
			docs = append(docs, doc)
		}
		return db.Collection("documents").InsertMany(transaction, docs)
	}, options.Transaction().SetWriteConcern(writeconcern.Majority()))
	require.NoError(t, err)
	positions := map[string]bool{request.Checkpoint: true}
	for _, expectedID := range []string{"txn-a", "txn-b", "txn-c"} {
		page, err := nodes[pulls%len(nodes)].Pull(ctx, "app", request)
		pulls++
		require.NoError(t, err)
		require.Len(t, page.Documents, 1)
		require.Equal(t, expectedID, page.Documents[0].GetID())
		require.Equal(t, int64(17), page.Documents[0]["updatedAt"])
		require.False(t, page.CaughtUp)
		require.False(t, positions[page.Checkpoint], "each consumed transaction event advances progress")
		positions[page.Checkpoint] = true
		apply(page)
	}
	emitted = nil
	drain()
	require.Empty(t, emitted)
	require.Len(t, mirror, 7)
}
