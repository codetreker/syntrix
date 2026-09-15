package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/database"
	storemongo "github.com/syntrixbase/syntrix/internal/core/storage/mongo"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	queryclient "github.com/syntrixbase/syntrix/internal/query/client"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	querygrpc "github.com/syntrixbase/syntrix/internal/query/grpc"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
	"google.golang.org/grpc"
)

func TestPullHTTPGRPCMongo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://localhost:27017").SetWriteConcern(writeconcern.Majority()))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		assert.NoError(t, mongoClient.Disconnect(cleanupCtx))
	})
	require.NoError(t, mongoClient.Ping(ctx, nil))
	db := mongoClient.Database(fmt.Sprintf("test_http_pull_%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		assert.NoError(t, db.Drop(cleanupCtx))
	})
	store := storemongo.NewDocumentStore(mongoClient, db, "documents", "sys", 0)
	require.NoError(t, store.(interface{ EnsureIndexes(context.Context) error }).EnsureIndexes(ctx))

	const namespace, canonicalID = "friendly-name", "canonical-id"
	largeText := strings.Repeat("x", 5<<20)
	require.NoError(t, store.Create(ctx, namespace, types.NewStoredDoc(namespace, "users", "alice", map[string]any{
		"count": int64(math.MaxInt64), "nested": map[string]any{"count": int64(9007199254740993)}, "text": largeText,
	})))
	require.NoError(t, store.Create(ctx, namespace, types.NewStoredDoc(namespace, "users", "bob", map[string]any{"count": int64(1)})))
	// The validated entity ID binds the cursor; document routing still uses the
	// existing namespace. A canonical-ID lookup would expose this other document.
	require.NoError(t, store.Create(ctx, canonicalID, types.NewStoredDoc(canonicalID, "users", "wrong-namespace", nil)))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := grpc.NewServer(grpc.MaxRecvMsgSize(wire.MaxGRPCBytes))
	pb.RegisterQueryServiceServer(grpcServer, querygrpc.NewServer(querycore.New(store, nil)))
	serveResult := make(chan error, 1)
	go func() { serveResult <- grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		assert.NoError(t, <-serveResult)
	})
	remote, err := queryclient.New(listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, remote.Close()) })

	var current atomic.Pointer[database.Database]
	slug := namespace
	current.Store(&database.Database{ID: canonicalID, Slug: &slug, OwnerID: "owner", Status: database.StatusActive})
	databaseService := &mockDatabaseService{resolveFunc: func(_ context.Context, identifier string) (*database.Database, error) {
		if identifier != namespace {
			return nil, database.ErrDatabaseNotFound
		}
		return current.Load(), nil
	}}
	auth := &pullRouteAuth{MockAuthService: new(MockAuthService), uid: "owner"}
	handler, err := NewHandler(remote, auth, new(AllowAllAuthzService), WithDatabaseService(databaseService))
	require.NoError(t, err)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	httpClient := httpServer.Client()
	httpClient.Timeout = 35 * time.Second

	requestPage := func(checkpoint string, expectedStatus int) ReplicaPullResponse {
		t.Helper()
		var position any
		if checkpoint != "" {
			position = checkpoint
		}
		body, err := json.Marshal(map[string]any{"collection": "users", "checkpoint": position, "limit": 1})
		require.NoError(t, err)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/replication/v1/databases/"+namespace+"/pull", bytes.NewReader(body))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		response, err := httpClient.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, expectedStatus, response.StatusCode, string(data))
		var page ReplicaPullResponse
		if expectedStatus != http.StatusOK {
			assert.Contains(t, string(data), `"code":"BAD_REQUEST"`)
			assert.NotContains(t, string(data), `"checkpoint"`)
			return page
		}
		require.NoError(t, json.Unmarshal(data, &page))
		require.NotEmpty(t, page.Checkpoint)
		require.LessOrEqual(t, len(page.Documents), 1)
		return page
	}

	mirror := map[string]model.Document{}
	var deletion model.Document
	drain := func(checkpoint string) string {
		t.Helper()
		for range 20 {
			page := requestPage(checkpoint, http.StatusOK)
			for _, raw := range page.Documents {
				decoded, err := model.DecodeTypedValue(raw)
				require.NoError(t, err)
				object, ok := decoded.(map[string]any)
				require.True(t, ok)
				document := model.Document(object)
				require.Equal(t, "users", document.GetCollection())
				require.NotEqual(t, "wrong-namespace", document.GetID())
				if document["deleted"] == true {
					deletion = document
					delete(mirror, document.GetID())
				} else {
					mirror[document.GetID()] = document
				}
			}
			checkpoint = page.Checkpoint
			if page.CaughtUp {
				return checkpoint
			}
		}
		t.Fatal("Pull did not reach a source watermark within 20 bounded pages")
		return ""
	}

	checkpoint := drain("")
	require.Len(t, mirror, 2)
	assert.Equal(t, int64(math.MaxInt64), mirror["alice"]["count"])
	assert.Equal(t, map[string]any{"count": int64(9007199254740993)}, mirror["alice"]["nested"])
	assert.Equal(t, largeText, mirror["alice"]["text"])
	assert.IsType(t, int64(0), mirror["alice"]["version"])
	require.NoError(t, store.Delete(ctx, namespace, "users/alice", nil))
	require.NoError(t, store.Update(ctx, namespace, "users/bob", map[string]any{"count": int64(math.MaxInt64)}, nil))
	checkpoint = drain(checkpoint)
	assert.Equal(t, model.Document{"id": "alice", "collection": "users", "deleted": true}, deletion)
	require.Len(t, mirror, 1)
	assert.Equal(t, int64(math.MaxInt64), mirror["bob"]["count"])

	current.Store(&database.Database{ID: "replacement-id", Slug: &slug, OwnerID: "owner", Status: database.StatusActive})
	requestPage(checkpoint, http.StatusBadRequest)
	current.Store(&database.Database{ID: canonicalID, Slug: &slug, OwnerID: "owner", Status: database.StatusActive})
	requestPage(checkpoint, http.StatusOK)
}
