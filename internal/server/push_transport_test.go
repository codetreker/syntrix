package server

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	queryclient "github.com/syntrixbase/syntrix/internal/query/client"
	querygrpc "github.com/syntrixbase/syntrix/internal/query/grpc"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type pushTransportService struct{ querygrpc.Service }

func (pushTransportService) Push(_ context.Context, database string, request storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	doc := *request.Changes[0].Doc
	doc.Database = database
	doc.Collection = request.Collection
	return &storage.ReplicationPushResponse{Conflicts: []storage.ReplicationPushConflict{{ID: "alice", Reason: storage.PushAlreadyExists, Current: &doc}}}, nil
}

func TestProductionServerPushTypedPayloadAboveFourMiB(t *testing.T) {
	values := make([]any, 150000)
	for i := range values {
		values[i] = float64(1)
	}
	request := storage.ReplicationPushRequest{Collection: "users", Changes: []storage.ReplicationPushChange{{Action: storage.PushCreate, Doc: &storage.StoredDoc{Fullpath: "users/alice", Data: map[string]interface{}{"values": values}}}}}
	plain, err := json.Marshal(request.Changes[0].Doc.Data)
	require.NoError(t, err)
	require.Less(t, len(plain), 1<<20)
	encoded, err := wire.EncodePushRequest("db", request)
	require.NoError(t, err)
	require.Greater(t, proto.Size(encoded), 4<<20)
	require.Less(t, proto.Size(encoded), wire.MaxGRPCBytes)

	service := New(Config{}, nil).(*serverImpl)
	pb.RegisterQueryServiceServer(service.grpcServer, querygrpc.NewServer(pushTransportService{}))
	listener := bufconn.Listen(1 << 20)
	done := make(chan error, 1)
	go func() { done <- service.grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		service.grpcServer.Stop()
		require.NoError(t, listener.Close())
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	client, err := queryclient.NewWithOptions("passthrough:///push-production", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	response, err := client.Push(ctx, "db", request)
	require.NoError(t, err)
	require.Len(t, response.Conflicts, 1)
	require.Equal(t, request.Changes[0].Doc.Data, response.Conflicts[0].Current.Data)

	request.Changes[0].Doc.Data = map[string]interface{}{"large": strings.Repeat("x", wire.MaxGRPCBytes)}
	_, err = client.Push(ctx, "db", request)
	require.ErrorIs(t, err, model.ErrInvalidQuery)
	// A raw caller cannot bypass the production receive bound.
	conn, err := grpc.NewClient("passthrough:///push-production", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	require.NoError(t, err)
	defer conn.Close()
	encoded.Changes[0].Document.Data = []byte(strings.Repeat("x", wire.MaxGRPCBytes+1))
	_, err = pb.NewQueryServiceClient(conn).Push(ctx, encoded)
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
}
