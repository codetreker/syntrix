package client_test

import (
	"context"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	querygrpc "github.com/syntrixbase/syntrix/internal/query/grpc"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"math"
	"testing"
)

type pushService struct {
	querygrpc.Service
	push func(context.Context, string, storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error)
}

func (s pushService) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	return s.push(ctx, database, req)
}

func TestPushTransportPresenceAndConflicts(t *testing.T) {
	req := storage.ReplicationPushRequest{Collection: "users", Changes: []storage.ReplicationPushChange{
		{Action: storage.PushUpdate, BaseVersion: proto.Int64(0), Doc: &storage.StoredDoc{Fullpath: "users/alice", Data: map[string]interface{}{"nested": map[string]interface{}{"large": int64(math.MaxInt64), "float": float64(1), "text": "exact"}}}},
		{Action: storage.PushDelete, BaseVersion: proto.Int64(9223372036854775807), Doc: &storage.StoredDoc{Fullpath: "users/alice"}},
		{Action: storage.PushCreate, Doc: &storage.StoredDoc{Fullpath: "users/bob"}},
	}}
	want := &storage.ReplicationPushResponse{Conflicts: []storage.ReplicationPushConflict{
		{ChangeIndex: 0, ID: "alice", Reason: storage.PushMissing},
		{ChangeIndex: 1, ID: "alice", Reason: storage.PushTombstoned, Current: &storage.StoredDoc{Database: "db", Collection: "users", Fullpath: "users/alice", Version: 9223372036854775807, Deleted: true, Data: map[string]interface{}{}}},
	}}
	want.Conflicts = append(want.Conflicts, storage.ReplicationPushConflict{ChangeIndex: 2, ID: "bob", Reason: storage.PushAlreadyExists, Current: &storage.StoredDoc{Database: "db", Collection: "users", Fullpath: "users/bob", Data: map[string]interface{}{"nested": map[string]interface{}{"large": int64(math.MaxInt64), "float": float64(1), "text": "exact"}}}})
	service := pushService{push: func(_ context.Context, database string, got storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
		require.Equal(t, "db", database)
		require.Equal(t, req, got)
		return want, nil
	}}
	client := newPullTransport(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 5e9)
	defer cancel()
	got, err := client.Push(ctx, "db", req)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestPushTransportErrorsAndValidation(t *testing.T) {
	req := storage.ReplicationPushRequest{Collection: "users", Changes: []storage.ReplicationPushChange{{Action: storage.PushUpdate, Doc: &storage.StoredDoc{Fullpath: "users/alice"}}}}
	for _, failure := range []error{model.ErrInvalidQuery, model.ErrNotFound, model.ErrExists, model.ErrPreconditionFailed, context.Canceled, context.DeadlineExceeded, status.Error(codes.Unavailable, "retry"), status.Error(codes.Aborted, "retry"), status.Error(codes.Internal, "internal")} {
		service := pushService{push: func(context.Context, string, storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
			return nil, failure
		}}
		client := newPullTransport(t, service)
		ctx, cancel := context.WithTimeout(context.Background(), 5e9)
		_, err := client.Push(ctx, "db", req)
		cancel()
		require.Error(t, err)
		if status.Code(failure) != codes.Unknown {
			require.Equal(t, status.Code(failure), status.Code(err))
		} else {
			require.ErrorIs(t, err, failure)
		}
	}
	calls := 0
	client := newPullTransport(t, pushService{push: func(context.Context, string, storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
		calls++
		return nil, nil
	}})
	_, err := client.Push(context.Background(), "", req)
	require.ErrorIs(t, err, model.ErrInvalidQuery)
	require.Zero(t, calls)
	_, err = client.Push(context.Background(), "db", req)
	require.Equal(t, codes.Internal, status.Code(err))
	require.Equal(t, 1, calls)
}

func TestPushCreateConditionsOverTransport(t *testing.T) {
	for _, condition := range []storage.CreateCondition{"", storage.CreateIfAbsent, storage.CreateIfTombstone} {
		req := storage.ReplicationPushRequest{Collection: "users", Changes: []storage.ReplicationPushChange{{Action: storage.PushCreate, CreateCondition: condition, Doc: &storage.StoredDoc{Fullpath: "users/alice"}}}}
		if condition == storage.CreateIfTombstone {
			req.Changes[0].BaseVersion = proto.Int64(math.MaxInt64)
		}
		client := newPullTransport(t, pushService{push: func(_ context.Context, database string, got storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
			require.Equal(t, "db", database)
			require.Equal(t, req, got)
			return &storage.ReplicationPushResponse{}, nil
		}})
		ctx, cancel := context.WithTimeout(context.Background(), 5e9)
		_, err := client.Push(ctx, "db", req)
		cancel()
		require.NoError(t, err)
	}
}
