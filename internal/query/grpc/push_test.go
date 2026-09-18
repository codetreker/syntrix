package grpc

import (
	"context"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func TestPushRejectsInvalidBatchBeforeService(t *testing.T) {
	for _, req := range []*pb.PushRequest{
		nil,
		{Database: "db", Collection: "users", Changes: []*pb.PushChange{{Action: pb.PushAction_PUSH_ACTION_UPDATE, Document: &pb.Document{Fullpath: "users/alice", Data: []byte(`{"type":"null"}`)}}, {Action: pb.PushAction_PUSH_ACTION_UPDATE, Document: &pb.Document{Fullpath: "users/bob", Data: []byte("{")}}}},
		{Database: "db", Collection: "users", Changes: []*pb.PushChange{{Action: pb.PushAction_PUSH_ACTION_UPDATE, Document: &pb.Document{Fullpath: "users/alice", Data: []byte(`{"name":"\ud800"}`)}}}},
		{Database: "db", Collection: "users", Changes: []*pb.PushChange{{Document: &pb.Document{Fullpath: "users/alice", Data: []byte(`{"type":"null"}`)}}}},
		{Database: "db", Collection: "users", Changes: []*pb.PushChange{{Action: pb.PushAction_PUSH_ACTION_UPDATE, Document: &pb.Document{Fullpath: "other/alice", Data: []byte(`{"type":"null"}`)}}}},
	} {
		service := new(MockService)
		_, err := NewServer(service).Push(context.Background(), req)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		service.AssertNotCalled(t, "Push")
	}
}

func TestPushServiceAndResponseErrors(t *testing.T) {
	for _, failure := range []error{model.ErrNotFound, status.Error(codes.Unavailable, "retry"), nil} {
		service := new(MockService)
		service.On("Push", mock.Anything, "db", mock.Anything).Return(nil, failure).Once()
		_, err := NewServer(service).Push(context.Background(), &pb.PushRequest{Database: "db", Collection: "users", Changes: []*pb.PushChange{{Action: pb.PushAction_PUSH_ACTION_UPDATE, Document: &pb.Document{Fullpath: "users/alice", Data: []byte(`{"type":"null"}`)}}}})
		require.Error(t, err)
		expected := codes.Internal
		if failure == model.ErrNotFound {
			expected = codes.NotFound
		} else if failure != nil {
			expected = codes.Unavailable
		}
		require.Equal(t, expected, status.Code(err))
		service.AssertExpectations(t)
	}
}

func TestPushOversizedConflictReturnsWorkLimit(t *testing.T) {
	service := new(MockService)
	response := &storage.ReplicationPushResponse{Conflicts: []storage.ReplicationPushConflict{{ID: "alice", Reason: storage.PushAlreadyExists, Current: &storage.StoredDoc{Database: "db", Collection: "users", Fullpath: "users/alice", Data: map[string]interface{}{"large": strings.Repeat("x", wire.MaxGRPCBytes)}}}}}
	service.On("Push", mock.Anything, "db", mock.Anything).Return(response, nil).Once()
	_, err := NewServer(service).Push(context.Background(), &pb.PushRequest{Database: "db", Collection: "users", Changes: []*pb.PushChange{{Action: pb.PushAction_PUSH_ACTION_CREATE, Document: &pb.Document{Fullpath: "users/alice", Data: []byte(`{"type":"null"}`)}}}})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	service.AssertExpectations(t)
}

func TestPushCreateConditionInvalidLaterChangeRejectsBeforeService(t *testing.T) {
	for _, bad := range []*pb.PushChange{
		{Action: pb.PushAction_PUSH_ACTION_CREATE, CreateCondition: pb.PushCreateCondition_PUSH_CREATE_CONDITION_UNSPECIFIED.Enum()},
		{Action: pb.PushAction_PUSH_ACTION_CREATE, CreateCondition: pb.PushCreateCondition(99).Enum()},
		{Action: pb.PushAction_PUSH_ACTION_UPDATE, CreateCondition: pb.PushCreateCondition_PUSH_CREATE_CONDITION_ABSENT.Enum()},
		{Action: pb.PushAction_PUSH_ACTION_DELETE, CreateCondition: pb.PushCreateCondition_PUSH_CREATE_CONDITION_TOMBSTONE.Enum(), BaseVersion: proto.Int64(1)},
		{Action: pb.PushAction_PUSH_ACTION_CREATE, CreateCondition: pb.PushCreateCondition_PUSH_CREATE_CONDITION_ABSENT.Enum(), BaseVersion: proto.Int64(0)},
		{Action: pb.PushAction_PUSH_ACTION_CREATE, CreateCondition: pb.PushCreateCondition_PUSH_CREATE_CONDITION_TOMBSTONE.Enum()},
	} {
		bad.Document = &pb.Document{Fullpath: "users/bad", Data: []byte(`{"type":"null"}`)}
		service := new(MockService)
		request := &pb.PushRequest{Database: "db", Collection: "users", Changes: []*pb.PushChange{{Action: pb.PushAction_PUSH_ACTION_CREATE, Document: &pb.Document{Fullpath: "users/first", Data: []byte(`{"type":"null"}`)}}, bad}}
		_, err := NewServer(service).Push(context.Background(), request)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		service.AssertNotCalled(t, "Push", mock.Anything, mock.Anything, mock.Anything)
	}
}
