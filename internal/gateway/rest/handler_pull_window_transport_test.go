package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/query"
	queryclient "github.com/syntrixbase/syntrix/internal/query/client"
	"github.com/syntrixbase/syntrix/internal/query/core"
	querygrpc "github.com/syntrixbase/syntrix/internal/query/grpc"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type windowRouteSource struct {
	types.DocumentStore
	mu        sync.Mutex
	documents map[string]*types.StoredDoc
}

func (s *windowRouteSource) GetMany(_ context.Context, namespace string, paths []string, options ...types.ReadOptions) ([]*types.StoredDoc, error) {
	read, err := types.ResolveReadOptions(options)
	if err != nil {
		return nil, err
	}
	if namespace != "app" || read.Consistency != types.ReadAuthoritative {
		return nil, errors.New("window must preserve namespace and read authoritative documents")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	documents := make([]*types.StoredDoc, len(paths))
	for i, path := range paths {
		documents[i] = s.documents[path]
	}
	return documents, nil
}

func (*windowRouteSource) Watch(context.Context, string, string, types.WatchCheckpoint, types.WatchOptions) (types.WatchStream, error) {
	return nil, errors.New("window must use Query without opening Watch")
}

type windowRouteIndex struct {
	indexer.Service
	manager *manager.Manager
	opens   atomic.Int64
}

func (i *windowRouteIndex) OpenCandidates(ctx context.Context, namespace string, plan manager.Plan) (manager.CandidateStream, error) {
	i.opens.Add(1)
	return i.manager.OpenCandidates(ctx, namespace, plan)
}

func TestWindowPullAcrossLocalAndRemoteGateways(t *testing.T) {
	indexStore := mem_store.New()
	t.Cleanup(func() { require.NoError(t, indexStore.Close()) })
	idx := &windowRouteIndex{manager: manager.New(indexStore)}
	require.NoError(t, idx.manager.LoadTemplatesFromBytes([]byte("templates:\n  - name: window\n    collectionPattern: users\n    includeDeleted: true\n    fields:\n      - {field: score, order: asc}\n")))
	source := &windowRouteSource{documents: map[string]*types.StoredDoc{}}
	payload := ""
	project := func(id string, score int64, deleted bool) {
		source.mu.Lock()
		defer source.mu.Unlock()
		doc := types.NewStoredDoc("app", "users", id, map[string]any{"score": score, "counter": int64(math.MaxInt64), "payload": payload})
		doc.Version = 1
		if prior := source.documents["users/"+id]; prior != nil {
			doc.Version = prior.Version + 1
		}
		doc.Deleted = deleted
		if deleted {
			doc.Data = map[string]any{}
		}
		source.documents["users/"+id] = &doc
		template := idx.manager.Templates()[0]
		projection, err := indexer.BuildDocumentProjection(&doc, &template, "ready-1", indexer.DefaultProjectionLimits())
		require.NoError(t, err)
		require.NoError(t, indexStore.ApplyDocumentProjection([]store.Projection{projection}, ""))
		require.NoError(t, indexStore.PublishGeneration(projection.Index, "ready"))
	}
	project("alice", 1, false)
	project("bob", 2, false)
	project("charlie", 3, false)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterQueryServiceServer(server, querygrpc.NewServer(core.New(source, idx)))
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil {
				require.ErrorIs(t, err, grpc.ErrServerStopped)
			}
		case <-time.After(5 * time.Second):
			t.Error("window Query server did not stop")
		}
	})
	remote, err := queryclient.NewWithOptions("passthrough:///window-pull", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, remote.Close()) })
	gateways := []*http.ServeMux{}
	for _, service := range []query.Service{core.New(source, idx), remote} {
		handler, err := NewHandler(service, &pullRouteAuth{MockAuthService: new(MockAuthService), uid: "owner"}, new(AllowAllAuthzService))
		require.NoError(t, err)
		handler.SetDatabaseService(querySourceRouteDatabase{})
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		gateways = append(gateways, mux)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body := `{"collection":"users","source":{"version":1,"filters":[],"orderBy":[{"field":"score","direction":"asc"}],"limit":2},"requestId":"refresh-1"}`
	lastGeneration, sourceHash := "", ""
	for turn, want := range [][]string{{"alice", "bob"}, {"charlie", "alice"}, {"alice", "bob"}, {}} {
		if turn == 1 {
			project("charlie", 0, false)
		}
		if turn == 2 {
			project("charlie", 0, true)
		}
		if turn == 3 {
			project("alice", 1, true)
			project("bob", 2, true)
		}
		for _, gateway := range gateways {
			r := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/app/pull", strings.NewReader(body)).WithContext(ctx)
			r.Header.Set("X-Syntrix-Expected-Database-Identity", "0123456789abcdef")
			response := newPullRecorder()
			before := idx.opens.Load()
			gateway.ServeHTTP(response, r)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Equal(t, before+1, idx.opens.Load(), "one Query candidate stream per window")
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &fields))
			require.Len(t, fields, 9)
			var result struct {
				ProtocolVersion  int               `json:"protocolVersion"`
				Mode             string            `json:"mode"`
				DatabaseIdentity string            `json:"databaseIdentity"`
				SourceHash       string            `json:"sourceHash"`
				RequestID        string            `json:"requestId"`
				GenerationID     string            `json:"generationId"`
				Complete         bool              `json:"complete"`
				EffectiveOrder   []model.Order     `json:"effectiveOrder"`
				Documents        []json.RawMessage `json:"documents"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Equal(t, 1, result.ProtocolVersion)
			require.Equal(t, "replace", result.Mode)
			require.Equal(t, "0123456789abcdef", result.DatabaseIdentity)
			require.Equal(t, "refresh-1", result.RequestID)
			require.True(t, result.Complete)
			require.NotEmpty(t, result.GenerationID)
			require.NotEqual(t, lastGeneration, result.GenerationID)
			lastGeneration = result.GenerationID
			if sourceHash == "" {
				sourceHash = result.SourceHash
				require.Len(t, sourceHash, 64)
			}
			require.Equal(t, sourceHash, result.SourceHash)
			require.Equal(t, []model.Order{{Field: "score", Direction: "asc"}, {Field: "id", Direction: "asc"}}, result.EffectiveOrder)
			ids := []string{}
			for _, encoded := range result.Documents {
				value, err := model.DecodeTypedValue(encoded)
				require.NoError(t, err)
				document := value.(map[string]any)
				require.Equal(t, int64(math.MaxInt64), document["counter"])
				ids = append(ids, document["id"].(string))
			}
			require.Equal(t, want, ids)
		}
	}
	for _, gateway := range gateways {
		r := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/app/pull", strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("X-Syntrix-Expected-Database-Identity", "fedcba9876543210")
		response := newPullRecorder()
		before := idx.opens.Load()
		gateway.ServeHTTP(response, r)
		require.Equal(t, http.StatusConflict, response.Code)
		require.Contains(t, response.Body.String(), "DATABASE_IDENTITY_MISMATCH")
		require.Equal(t, before, idx.opens.Load())
	}
	payload = strings.Repeat("x", 900<<10)
	for i := range 6 {
		project(fmt.Sprintf("large-%d", i), int64(i), false)
	}
	largeBody := strings.Replace(body, `"limit":2`, `"limit":6`, 1)
	for _, gateway := range gateways {
		r := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/app/pull", strings.NewReader(largeBody)).WithContext(ctx)
		r.Header.Set("X-Syntrix-Expected-Database-Identity", "0123456789abcdef")
		response := newPullRecorder()
		gateway.ServeHTTP(response, r)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Greater(t, response.Body.Len(), 4<<20)
		var result struct {
			Complete  bool              `json:"complete"`
			Documents []json.RawMessage `json:"documents"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		require.True(t, result.Complete)
		require.Len(t, result.Documents, 6)
		for i, encoded := range result.Documents {
			value, err := model.DecodeTypedValue(encoded)
			require.NoError(t, err)
			document := value.(map[string]any)
			require.Equal(t, fmt.Sprintf("large-%d", i), document["id"])
			require.Equal(t, payload, document["payload"])
			require.Equal(t, int64(math.MaxInt64), document["counter"])
		}
	}
}
