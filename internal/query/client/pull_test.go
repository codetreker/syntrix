package client_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	queryclient "github.com/syntrixbase/syntrix/internal/query/client"
	querygrpc "github.com/syntrixbase/syntrix/internal/query/grpc"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type pullService struct {
	querygrpc.Service
	pull func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error)
}

func (s pullService) Pull(ctx context.Context, database string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	return s.pull(ctx, database, request)
}

func newPullTransport(t *testing.T, service querygrpc.Service, options ...grpc.DialOption) *queryclient.Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterQueryServiceServer(server, querygrpc.NewServer(service))
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, listener.Close())
		select {
		case err := <-serveDone:
			if err != nil {
				require.ErrorIs(t, err, grpc.ErrServerStopped)
			}
		case <-time.After(5 * time.Second):
			t.Error("query gRPC server did not stop")
		}
	})
	options = append(options, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	client, err := queryclient.NewWithOptions("passthrough:///replication-pull", options...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}

func TestPullGRPCPreservesLargeTypedPage(t *testing.T) {
	page := &storage.ReplicationPullResponse{Checkpoint: "opaque-next", CaughtUp: true}
	for i := range 6 {
		page.Documents = append(page.Documents, model.Document{
			"id": fmt.Sprintf("doc-%d", i), "collection": "users", "payload": strings.Repeat("x", 900<<10),
			"version": int64(math.MaxInt64), "nested": []any{int64(math.MinInt64), map[string]any{"type": "int64", "value": "business"}},
		})
	}
	page.Documents = append(page.Documents, model.Document{"id": "deleted", "collection": "users", "deleted": true})
	encoded, err := wire.EncodePullPage(page)
	require.NoError(t, err)
	require.Greater(t, proto.Size(encoded), 4<<20)
	require.Less(t, proto.Size(encoded), wire.MaxGRPCBytes)
	jsonPage, err := wire.EncodeJSONPullPage(page)
	require.NoError(t, err)
	require.Less(t, len(jsonPage), wire.MaxPageBytes)
	service := pullService{pull: func(_ context.Context, database string, request storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		if database != "database1" || request.DatabaseIdentity != "resolved-entity" || request.Collection != "users" || request.Checkpoint != "" || request.Limit != 100 {
			return nil, errors.New("unexpected pull request")
		}
		return page, nil
	}}
	client := newPullTransport(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := storage.ReplicationPullRequest{Collection: "users", Limit: 100, DatabaseIdentity: "resolved-entity"}
	local, err := service.Pull(ctx, "database1", request)
	require.NoError(t, err)
	remote, err := client.Pull(ctx, "database1", request)
	require.NoError(t, err)
	assert.Equal(t, local, remote)
	assert.Len(t, remote.Documents[6], 3)
}

func TestPullGRPCSourceErrorsRemainTyped(t *testing.T) {
	t.Run("page budget", func(t *testing.T) {
		client := newPullTransport(t, pullService{pull: func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
			return nil, model.ErrQueryWorkLimit
		}})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		page, err := client.Pull(ctx, "database1", storage.ReplicationPullRequest{Collection: "users"})
		require.Nil(t, page)
		require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	})
	for _, code := range []types.WatchErrorCode{
		types.WatchHistoryUnavailable, types.WatchSourceMismatch, types.WatchPayloadUnavailable,
		types.WatchPermissionDenied, types.WatchUnsupported, types.WatchSourceUnavailable,
		types.WatchInvalidEvent,
	} {
		t.Run(string(code), func(t *testing.T) {
			client := newPullTransport(t, pullService{pull: func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
				return nil, &types.WatchError{Code: code, Database: "private-source", Cause: errors.New("secret resume token")}
			}})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			page, err := client.Pull(ctx, "database1", storage.ReplicationPullRequest{Collection: "users"})
			require.Nil(t, page)
			var failure *types.WatchError
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, code, failure.Code)
			assert.NotContains(t, err.Error(), "secret")
			assert.NotContains(t, err.Error(), "private-source")
		})
	}
}

func TestPullGRPCRejectsInvalidRequestBeforeService(t *testing.T) {
	for _, request := range []*pb.PullRequest{
		{Database: "database1", Collection: "users"},
		{Database: "database1", Collection: "users", WireVersion: wire.Version, Limit: proto.Int32(-1)},
		{Database: "database1", Collection: "users", WireVersion: wire.Version, Checkpoint: proto.String("malformed")},
	} {
		service := pullService{pull: func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
			t.Error("invalid request reached query service")
			return nil, errors.New("unexpected source access")
		}}
		response, err := querygrpc.NewServer(service).Pull(context.Background(), request)
		assert.Nil(t, response)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	}
}

func TestPullGRPCQuerySourceLargeTypedEvents(t *testing.T) {
	page := &storage.ReplicationPullResponse{ProtocolVersion: 1, Mode: "events", DatabaseIdentity: "resolved-entity", SourceHash: strings.Repeat("a", 64), GenerationID: "generation", Phase: "live", BootstrapComplete: true, Checkpoint: "cp", CaughtUp: true, Events: []types.ReplicationEvent{}}
	for i := range 6 {
		page.Events = append(page.Events, types.ReplicationEvent{Type: types.ReplicationUpsert, Document: model.Document{"id": fmt.Sprintf("doc-%d", i), "collection": "users", "payload": strings.Repeat("x", 900<<10), "version": int64(math.MaxInt64)}})
	}
	page.Events = append(page.Events, types.ReplicationEvent{Type: types.ReplicationLeave, ID: "left"}, types.ReplicationEvent{Type: types.ReplicationDelete, ID: "deleted"})
	source := &types.ReplicationSource{Version: 1, Filters: model.Filters{{Field: "version", Op: model.OpEq, Value: int64(math.MaxInt64)}}}
	request := storage.ReplicationPullRequest{Collection: "users", DatabaseIdentity: "resolved-entity", Source: source, Limit: 100}
	client := newPullTransport(t, pullService{pull: func(_ context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		require.Equal(t, "database1", database)
		require.Equal(t, request, req)
		return page, nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	remote, err := client.Pull(ctx, "database1", request)
	require.NoError(t, err)
	require.Equal(t, page, remote)
	encoded, err := wire.EncodePullPage(page)
	require.NoError(t, err)
	require.Greater(t, proto.Size(encoded), 4<<20)
}

func TestPullGRPCQuerySourceRejectsMalformedAndDowngrade(t *testing.T) {
	for _, source := range []*pb.ReplicationSource{
		{Version: 2}, {Version: 1, Filters: []*pb.Filter{{Field: "score", Op: "==", Value: []byte(`{"type":"int64","value":"1","value":"2"}`)}}},
		{Version: 1, Filters: []*pb.Filter{{Field: "score", Op: "=="}}}, {Version: 1, Limit: proto.Int32(0)},
	} {
		service := pullService{pull: func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
			t.Fatal("invalid source reached service")
			return nil, nil
		}}
		page, err := querygrpc.NewServer(service).Pull(context.Background(), &pb.PullRequest{Database: "db", DatabaseIdentity: "entity", Collection: "users", WireVersion: wire.Version, Source: source})
		require.Nil(t, page)
		require.ErrorIs(t, wire.ReplicationStatusToError(err), types.ErrInvalidReplicationSource)
	}
	client := newPullTransport(t, pullService{pull: func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
		return &storage.ReplicationPullResponse{Checkpoint: "cp"}, nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	page, err := client.Pull(ctx, "db", storage.ReplicationPullRequest{Collection: "users", DatabaseIdentity: "entity", Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{}}})
	require.Nil(t, page)
	var failure *types.WatchError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, types.WatchInvalidEvent, failure.Code)
}

func TestPullGRPCWindowRejectsExplicitProgressFields(t *testing.T) {
	for _, field := range []string{"checkpoint", "limit"} {
		t.Run(field, func(t *testing.T) {
			request := &pb.PullRequest{WireVersion: wire.Version, Database: "db", DatabaseIdentity: "entity", Collection: "users", Source: &pb.ReplicationSource{Version: 1, Limit: proto.Int32(5)}, RequestId: proto.String("request")}
			if field == "checkpoint" {
				request.Checkpoint = proto.String("")
			} else {
				request.Limit = proto.Int32(0)
			}
			raw, err := proto.Marshal(request)
			require.NoError(t, err)
			var decoded pb.PullRequest
			require.NoError(t, proto.Unmarshal(raw, &decoded))
			service := pullService{pull: func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
				t.Error("forbidden field reached service")
				return nil, nil
			}}
			response, err := querygrpc.NewServer(service).Pull(context.Background(), &decoded)
			require.Nil(t, response)
			require.ErrorIs(t, wire.ReplicationStatusToError(err), types.ErrInvalidReplicationSource)
		})
	}
}

func TestPullGRPCWindowErrorCategories(t *testing.T) {
	limit := 5
	request := storage.ReplicationPullRequest{Collection: "users", DatabaseIdentity: "entity", Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{}, Limit: &limit}, RequestID: proto.String("request")}
	for _, failure := range []error{types.ErrReplicationWindowIncomplete, indexer.ErrNoMatchingIndex, indexer.ErrIndexNotReady, indexer.ErrIndexRebuilding, model.ErrQueryWorkLimit} {
		t.Run(failure.Error(), func(t *testing.T) {
			client := newPullTransport(t, pullService{pull: func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
				return nil, fmt.Errorf("private query detail: %w", failure)
			}})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response, err := client.Pull(ctx, "db", request)
			require.Nil(t, response)
			require.ErrorIs(t, err, failure)
			require.NotContains(t, err.Error(), "private query detail")
		})
	}
}
