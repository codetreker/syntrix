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
	for _, code := range []types.ReplicationErrorCode{
		types.ReplicationHistoryUnavailable, types.ReplicationSourceMismatch, types.ReplicationIdentityUnavailable,
		types.ReplicationPermissionDenied, types.ReplicationUnsupported, types.ReplicationUnavailable,
		types.ReplicationBudgetExceeded, types.ReplicationInvalidState,
	} {
		t.Run(string(code), func(t *testing.T) {
			client := newPullTransport(t, pullService{pull: func(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
				return nil, &types.ReplicationError{Code: code, Database: "private-source", Cause: errors.New("secret resume token")}
			}})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			page, err := client.Pull(ctx, "database1", storage.ReplicationPullRequest{Collection: "users"})
			require.Nil(t, page)
			var failure *types.ReplicationError
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
		{Database: "database1", Collection: "users", WireVersion: wire.Version, Limit: -1},
		{Database: "database1", Collection: "users", WireVersion: wire.Version, Checkpoint: "malformed"},
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
