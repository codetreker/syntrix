package rest

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/query"
	queryclient "github.com/syntrixbase/syntrix/internal/query/client"
	"github.com/syntrixbase/syntrix/internal/query/core"
	querygrpc "github.com/syntrixbase/syntrix/internal/query/grpc"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type querySourceRouteDatabase struct{ database.Service }

func (querySourceRouteDatabase) ResolveDatabase(context.Context, string) (*database.Database, error) {
	return nil, errors.New("query-source binding must bypass cached resolution")
}

func (querySourceRouteDatabase) ResolveDatabaseAuthoritative(_ context.Context, namespace string) (*database.Database, error) {
	if namespace != "app" {
		return nil, errors.New("storage namespace changed")
	}
	slug := "app"
	return &database.Database{ID: "0123456789abcdef", Slug: &slug, OwnerID: "owner", Status: database.StatusActive}, nil
}

type querySourceRouteStore struct{ types.DocumentStore }

func querySourceRouteDocument(id string, active bool) *types.StoredDoc {
	doc := types.NewStoredDoc("app", "users", id, map[string]any{"active": active, "counter": int64(math.MaxInt64)})
	doc.Version = 1
	return &doc
}

func (querySourceRouteStore) ScanDocuments(_ context.Context, namespace string, request types.SourceScanRequest) (types.SourceScanPage, error) {
	if namespace != "app" || request.AtLeast != "C0" || request.Consistency != types.ReadAuthoritative {
		return types.SourceScanPage{}, errors.New("invalid scan boundary or namespace")
	}
	page := types.SourceScanPage{Exhausted: true, NextAfter: request.AfterID}
	for _, doc := range []*types.StoredDoc{querySourceRouteDocument("alice", true), querySourceRouteDocument("bob", false)} {
		id, err := types.LogicalDocumentID(doc)
		if err != nil {
			return page, err
		}
		if id <= request.AfterID {
			continue
		}
		page.Documents = append(page.Documents, doc)
		page.NextAfter = id
		page.Bytes += 100
		if len(page.Documents) == request.Limit {
			page.Exhausted = false
			break
		}
	}
	return page, nil
}

type querySourceRouteStream struct {
	initial types.WatchCheckpoint
	frames  []types.WatchFrame
}

func (s *querySourceRouteStream) InitialCheckpoint() types.WatchCheckpoint { return s.initial }
func (*querySourceRouteStream) Close() error                               { return nil }
func (s *querySourceRouteStream) Next(ctx context.Context) (types.WatchFrame, error) {
	if err := ctx.Err(); err != nil {
		return types.WatchFrame{}, err
	}
	if len(s.frames) == 0 {
		return types.WatchFrame{Checkpoint: "C4", CaughtUp: true}, nil
	}
	frame := s.frames[0]
	s.frames = s.frames[1:]
	return frame, nil
}

func (querySourceRouteStore) Watch(_ context.Context, namespace, collection string, after types.WatchCheckpoint, options types.WatchOptions) (types.WatchStream, error) {
	if namespace != "app" || collection != "users" {
		return nil, errors.New("invalid watch scope")
	}
	if options.StartMode == types.WatchStartForScan {
		return &querySourceRouteStream{initial: "C0"}, nil
	}
	frames := []types.WatchFrame{
		{Checkpoint: "C1", SourceBytes: 100, Event: &types.Event{Database: "app", Collection: "users", DocumentID: "alice", Type: types.EventUpdate, Document: querySourceRouteDocument("alice", false)}},
		{Checkpoint: "C2", SourceBytes: 100, Event: &types.Event{Database: "app", Collection: "users", DocumentID: "bob", Type: types.EventUpdate, Document: querySourceRouteDocument("bob", true)}},
		{Checkpoint: "C3", SourceBytes: 100, Event: &types.Event{Database: "app", Collection: "users", DocumentID: "alice", Type: types.EventDelete}},
		{Checkpoint: "C4", CaughtUp: true},
	}
	for len(frames) > 0 && frames[0].Checkpoint <= after {
		frames = frames[1:]
	}
	return &querySourceRouteStream{initial: after, frames: frames}, nil
}

func TestQuerySourcePullAcrossLocalAndRemoteGateways(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterQueryServiceServer(server, querygrpc.NewServer(core.New(querySourceRouteStore{}, nil)))
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		select {
		case err := <-serveDone:
			if err != nil {
				require.ErrorIs(t, err, grpc.ErrServerStopped)
			}
		case <-time.After(5 * time.Second):
			t.Error("query gRPC server did not stop")
		}
	})
	remote, err := queryclient.NewWithOptions("passthrough:///source-pull", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, remote.Close()) })
	gateways := make([]*http.ServeMux, 0, 2)
	for _, service := range []query.Service{core.New(querySourceRouteStore{}, nil), remote} {
		auth := &pullRouteAuth{MockAuthService: new(MockAuthService), uid: "owner"}
		handler, err := NewHandler(service, auth, new(AllowAllAuthzService))
		require.NoError(t, err)
		handler.SetDatabaseService(querySourceRouteDatabase{})
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		gateways = append(gateways, mux)
	}
	checkpoint, generation, sourceHash := "", "", ""
	eventKinds := []string{}
	caughtUp := false
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for page := 0; page < 10 && !caughtUp; page++ {
		body, err := json.Marshal(map[string]any{
			"collection": "users", "limit": 2, "checkpoint": checkpoint,
			"source": map[string]any{"version": 1, "filters": []any{map[string]any{
				"field": "active", "op": "==", "value": map[string]any{"type": "bool", "value": true},
			}}},
		})
		require.NoError(t, err)
		r := httptest.NewRequest(http.MethodPost, "/replication/v1/databases/app/pull", strings.NewReader(string(body))).WithContext(ctx)
		if page > 0 {
			r.Header.Set("X-Syntrix-Expected-Database-Identity", "0123456789abcdef")
		}
		response := newPullRecorder()
		gateways[page%len(gateways)].ServeHTTP(response, r)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var result struct {
			ProtocolVersion   int    `json:"protocolVersion"`
			Mode              string `json:"mode"`
			DatabaseIdentity  string `json:"databaseIdentity"`
			SourceHash        string `json:"sourceHash"`
			GenerationID      string `json:"generationId"`
			Phase             string `json:"phase"`
			Checkpoint        string `json:"checkpoint"`
			CaughtUp          bool   `json:"caughtUp"`
			BootstrapComplete bool   `json:"bootstrapComplete"`
			Events            []struct {
				Type     string          `json:"type"`
				ID       string          `json:"id"`
				Document json.RawMessage `json:"document"`
			} `json:"events"`
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		require.Equal(t, 1, result.ProtocolVersion)
		require.Equal(t, "events", result.Mode)
		require.Equal(t, "0123456789abcdef", result.DatabaseIdentity)
		if page == 0 {
			generation, sourceHash = result.GenerationID, result.SourceHash
			require.NotEmpty(t, generation)
			require.Len(t, sourceHash, 64)
		}
		require.Equal(t, generation, result.GenerationID)
		require.Equal(t, sourceHash, result.SourceHash)
		require.Equal(t, result.CaughtUp, result.BootstrapComplete)
		for _, event := range result.Events {
			eventKinds = append(eventKinds, event.Type)
			if event.Type == "upsert" {
				value, err := model.DecodeTypedValue(event.Document)
				require.NoError(t, err)
				require.Equal(t, int64(math.MaxInt64), value.(map[string]any)["counter"])
			} else {
				require.Equal(t, "alice", event.ID)
				require.Empty(t, event.Document)
			}
		}
		checkpoint, caughtUp = result.Checkpoint, result.CaughtUp
		if caughtUp {
			require.Equal(t, "live", result.Phase)
		}
	}
	require.True(t, caughtUp)
	require.Equal(t, []string{"upsert", "leave", "upsert", "delete"}, eventKinds)
}
