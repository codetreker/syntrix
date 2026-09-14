package grpc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// MockService is a mock implementation of the Service interface.
type MockService struct {
	mock.Mock
}

func (m *MockService) GetDocument(ctx context.Context, database string, path string) (model.Document, error) {
	args := m.Called(ctx, database, path)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockService) CreateDocument(ctx context.Context, database string, doc model.Document) error {
	args := m.Called(ctx, database, doc)
	return args.Error(0)
}

func (m *MockService) ReplaceDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	args := m.Called(ctx, database, data, pred)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockService) PatchDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	args := m.Called(ctx, database, data, pred)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(model.Document), args.Error(1)
}

func (m *MockService) DeleteDocument(ctx context.Context, database string, path string, pred model.Filters) error {
	args := m.Called(ctx, database, path, pred)
	return args.Error(0)
}

func (m *MockService) ExecuteQueryPage(ctx context.Context, database string, q model.Query) (model.QueryPage, error) {
	args := m.Called(ctx, database, q)
	if args.Get(0) == nil {
		return model.QueryPage{}, args.Error(1)
	}
	return args.Get(0).(model.QueryPage), args.Error(1)
}

func (m *MockService) ExecuteQuery(ctx context.Context, database string, q model.Query) ([]model.Document, error) {
	args := m.Called(ctx, database, q)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]model.Document), args.Error(1)
}

func (m *MockService) Pull(ctx context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	args := m.Called(ctx, database, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.ReplicationPullResponse), args.Error(1)
}

func (m *MockService) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	args := m.Called(ctx, database, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*storage.ReplicationPushResponse), args.Error(1)
}

func TestServer_GetDocument(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		expectedDoc := model.Document{
			"id":         "user1",
			"collection": "users",
			"name":       "Alice",
		}
		mockSvc.On("GetDocument", mock.Anything, "database1", "users/user1").Return(expectedDoc, nil)

		resp, err := server.GetDocument(context.Background(), &pb.GetDocumentRequest{
			Database: "database1",
			Path:     "users/user1",
		})

		assert.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Equal(t, "user1", resp.Document.Id)
		mockSvc.AssertExpectations(t)
	})

	t.Run("not found", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		mockSvc.On("GetDocument", mock.Anything, "database1", "users/missing").Return(nil, model.ErrNotFound)

		resp, err := server.GetDocument(context.Background(), &pb.GetDocumentRequest{
			Database: "database1",
			Path:     "users/missing",
		})

		assert.Nil(t, resp)
		assert.Error(t, err)
		st, ok := status.FromError(err)
		assert.True(t, ok)
		assert.Equal(t, codes.NotFound, st.Code())
	})
}

func TestServer_CreateDocument(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		mockSvc.On("CreateDocument", mock.Anything, "database1", mock.AnythingOfType("model.Document")).Return(nil)

		resp, err := server.CreateDocument(context.Background(), &pb.CreateDocumentRequest{
			Database: "database1",
			Document: &pb.Document{
				Id:         "user1",
				Collection: "users",
			},
		})

		assert.NoError(t, err)
		assert.NotNil(t, resp)
		mockSvc.AssertExpectations(t)
	})

	t.Run("already exists", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		mockSvc.On("CreateDocument", mock.Anything, "database1", mock.AnythingOfType("model.Document")).Return(model.ErrExists)

		resp, err := server.CreateDocument(context.Background(), &pb.CreateDocumentRequest{
			Database: "database1",
			Document: &pb.Document{Id: "user1"},
		})

		assert.Nil(t, resp)
		assert.Error(t, err)
		st, _ := status.FromError(err)
		assert.Equal(t, codes.AlreadyExists, st.Code())
	})
}

func TestServer_ReplaceDocument(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		resultDoc := model.Document{"id": "user1", "name": "Updated"}
		mockSvc.On("ReplaceDocument", mock.Anything, "database1", mock.AnythingOfType("model.Document"), mock.AnythingOfType("model.Filters")).Return(resultDoc, nil)

		resp, err := server.ReplaceDocument(context.Background(), &pb.ReplaceDocumentRequest{
			Database: "database1",
			Document: &pb.Document{Id: "user1"},
		})

		assert.NoError(t, err)
		assert.NotNil(t, resp)
		assert.Equal(t, "user1", resp.Document.Id)
	})

	t.Run("precondition failed", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		mockSvc.On("ReplaceDocument", mock.Anything, "database1", mock.AnythingOfType("model.Document"), mock.AnythingOfType("model.Filters")).Return(nil, model.ErrPreconditionFailed)

		resp, err := server.ReplaceDocument(context.Background(), &pb.ReplaceDocumentRequest{
			Database: "database1",
			Document: &pb.Document{Id: "user1"},
		})

		assert.Nil(t, resp)
		assert.Error(t, err)
		st, _ := status.FromError(err)
		assert.Equal(t, codes.FailedPrecondition, st.Code())
	})
}

func TestServer_PatchDocument(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		resultDoc := model.Document{"id": "user1", "name": "Patched"}
		mockSvc.On("PatchDocument", mock.Anything, "database1", mock.AnythingOfType("model.Document"), mock.AnythingOfType("model.Filters")).Return(resultDoc, nil)

		resp, err := server.PatchDocument(context.Background(), &pb.PatchDocumentRequest{
			Database: "database1",
			Document: &pb.Document{Id: "user1"},
		})

		assert.NoError(t, err)
		assert.NotNil(t, resp)
	})
}

func TestServer_DeleteDocument(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		mockSvc.On("DeleteDocument", mock.Anything, "database1", "users/user1", mock.AnythingOfType("model.Filters")).Return(nil)

		resp, err := server.DeleteDocument(context.Background(), &pb.DeleteDocumentRequest{
			Database: "database1",
			Path:     "users/user1",
		})

		assert.NoError(t, err)
		assert.NotNil(t, resp)
	})

	t.Run("not found", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		mockSvc.On("DeleteDocument", mock.Anything, "database1", "users/missing", mock.AnythingOfType("model.Filters")).Return(model.ErrNotFound)

		resp, err := server.DeleteDocument(context.Background(), &pb.DeleteDocumentRequest{
			Database: "database1",
			Path:     "users/missing",
		})

		assert.Nil(t, resp)
		assert.Error(t, err)
		st, _ := status.FromError(err)
		assert.Equal(t, codes.NotFound, st.Code())
	})
}

func TestServer_ExecuteQuery(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		docs := []model.Document{
			{"id": "doc1", "name": "First"},
			{"id": "doc2", "name": "Second"},
		}
		mockSvc.On("ExecuteQueryPage", mock.Anything, "database1", mock.AnythingOfType("model.Query")).Return(model.QueryPage{Documents: docs}, nil)

		resp, err := server.ExecuteQuery(context.Background(), &pb.ExecuteQueryRequest{
			Database:    "database1",
			WireVersion: 2,
			Query: &pb.Query{
				Collection: "docs",
				Limit:      10,
			},
		})

		assert.NoError(t, err)
		assert.Len(t, resp.Documents, 2)
	})

	t.Run("invalid query", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		queryErr := fmt.Errorf("%w: operator %q is not supported by indexed queries", model.ErrInvalidQuery, "in")
		mockSvc.On("ExecuteQueryPage", mock.Anything, "database1", mock.AnythingOfType("model.Query")).Return(nil, queryErr)

		resp, err := server.ExecuteQuery(context.Background(), &pb.ExecuteQueryRequest{
			Database:    "database1",
			WireVersion: 2,
			Query:       &pb.Query{Collection: ""},
		})

		assert.Nil(t, resp)
		assert.Error(t, err)
		st, _ := status.FromError(err)
		assert.Equal(t, codes.InvalidArgument, st.Code())
		assert.Equal(t, queryErr.Error(), st.Message())
	})
}

func TestServer_Pull(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		pullResp := &storage.ReplicationPullResponse{
			Documents: []model.Document{
				{"id": "doc1", "collection": "users"},
			},
			Checkpoint: "opaque",
			CaughtUp:   true,
		}
		mockSvc.On("Pull", mock.Anything, "database1", mock.Anything).Return(pullResp, nil)

		resp, err := server.Pull(context.Background(), &pb.PullRequest{
			Database:    "database1",
			Collection:  "users",
			Checkpoint:  "",
			WireVersion: 2,
			Limit:       100,
		})

		assert.NoError(t, err)
		assert.Len(t, resp.Documents, 1)
		assert.Equal(t, "opaque", resp.Checkpoint)
		assert.True(t, resp.CaughtUp)
		assert.EqualValues(t, 2, resp.WireVersion)
	})
}

func TestServerPullRejectsLegacyNumericProtobufBeforeService(t *testing.T) {
	legacy := protowire.AppendTag(nil, 1, protowire.BytesType)
	legacy = protowire.AppendString(legacy, "db")
	legacy = protowire.AppendTag(legacy, 2, protowire.BytesType)
	legacy = protowire.AppendString(legacy, "users")
	legacy = protowire.AppendTag(legacy, 3, protowire.VarintType)
	legacy = protowire.AppendVarint(legacy, 1700000000000)
	legacy = protowire.AppendTag(legacy, 4, protowire.VarintType)
	legacy = protowire.AppendVarint(legacy, 100)

	var request pb.PullRequest
	require.NoError(t, proto.Unmarshal(legacy, &request))
	require.Equal(t, "db", request.Database)
	require.Equal(t, "users", request.Collection)
	require.Empty(t, request.Checkpoint)
	require.Zero(t, request.WireVersion)
	require.NotEmpty(t, request.ProtoReflect().GetUnknown())

	service := new(MockService)
	page, err := NewServer(service).Pull(context.Background(), &request)
	require.Nil(t, page)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	details := status.Convert(err).Details()
	require.Len(t, details, 1)
	info, ok := details[0].(*errdetails.ErrorInfo)
	require.True(t, ok)
	require.Equal(t, "syntrix.replication", info.Domain)
	require.Equal(t, string(types.ReplicationInvalidCursor), info.Reason)
	service.AssertNotCalled(t, "Pull", mock.Anything, mock.Anything, mock.Anything)
}

func TestServerPullRejectsInvalidRequestsBeforeService(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name    string
		ctx     context.Context
		request *pb.PullRequest
		code    codes.Code
	}{
		{name: "canceled", ctx: ctx, request: &pb.PullRequest{Database: "db", Collection: "users", WireVersion: 2}, code: codes.Canceled},
		{name: "missing request", ctx: context.Background(), code: codes.InvalidArgument},
		{name: "document path", ctx: context.Background(), request: &pb.PullRequest{Database: "db", Collection: "users/alice", WireVersion: 2}, code: codes.InvalidArgument},
		{name: "oversized envelope", ctx: context.Background(), request: &pb.PullRequest{Database: "db", Collection: "users", DatabaseIdentity: strings.Repeat("x", (1<<20)+1), WireVersion: 2}, code: codes.InvalidArgument},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := new(MockService)
			page, err := NewServer(service).Pull(test.ctx, test.request)
			require.Nil(t, page)
			require.Equal(t, test.code, status.Code(err))
			service.AssertNotCalled(t, "Pull", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestServerPullDiscardsFailedResponses(t *testing.T) {
	for _, test := range []struct {
		name                string
		response            *storage.ReplicationPullResponse
		err                 error
		cancelDuringService bool
		code                codes.Code
	}{
		{name: "source history unavailable", err: &types.ReplicationError{Code: types.ReplicationHistoryUnavailable, Cause: fmt.Errorf("private native position")}, code: codes.FailedPrecondition},
		{name: "missing response checkpoint", response: &storage.ReplicationPullResponse{}, code: codes.Internal},
		{name: "canceled during source read", response: &storage.ReplicationPullResponse{Checkpoint: "opaque", CaughtUp: true}, cancelDuringService: true, code: codes.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			service := new(MockService)
			service.On("Pull", mock.Anything, "db", mock.Anything).Run(func(mock.Arguments) {
				if test.cancelDuringService {
					cancel()
				}
			}).Return(test.response, test.err).Once()
			page, err := NewServer(service).Pull(ctx, &pb.PullRequest{Database: "db", Collection: "users", WireVersion: 2})
			require.Nil(t, page)
			require.Equal(t, test.code, status.Code(err))
			require.NotContains(t, err.Error(), "private native position")
			service.AssertExpectations(t)
		})
	}
}

func TestServer_Push(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockSvc := new(MockService)
		server := NewServer(mockSvc)

		pushResp := &storage.ReplicationPushResponse{
			Conflicts: nil,
		}
		mockSvc.On("Push", mock.Anything, "database1", mock.Anything).Return(pushResp, nil)

		resp, err := server.Push(context.Background(), &pb.PushRequest{
			Database:   "database1",
			Collection: "users",
			Changes:    []*pb.PushChange{},
		})

		assert.NoError(t, err)
		assert.NotNil(t, resp)
	})
}

func TestErrorToStatus(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
	}{
		{"nil error", nil, codes.OK},
		{"not found", model.ErrNotFound, codes.NotFound},
		{"precondition failed", model.ErrPreconditionFailed, codes.FailedPrecondition},
		{"exists", model.ErrExists, codes.AlreadyExists},
		{"invalid query", model.ErrInvalidQuery, codes.InvalidArgument},
		{"permission denied", model.ErrPermissionDenied, codes.PermissionDenied},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), codes.DeadlineExceeded},
		{"canceled", fmt.Errorf("query: %w", context.Canceled), codes.Canceled},
		{"unknown error", assert.AnError, codes.Internal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := errorToStatus(tt.err)
			if tt.err == nil {
				assert.Nil(t, result)
			} else {
				st, ok := status.FromError(result)
				assert.True(t, ok)
				assert.Equal(t, tt.wantCode, st.Code())
			}
		})
	}
}

func TestStatusToError(t *testing.T) {
	tests := []struct {
		name    string
		code    codes.Code
		wantErr error
	}{
		{"not found", codes.NotFound, model.ErrNotFound},
		{"precondition failed", codes.FailedPrecondition, model.ErrPreconditionFailed},
		{"already exists", codes.AlreadyExists, model.ErrExists},
		{"invalid argument", codes.InvalidArgument, model.ErrInvalidQuery},
		{"permission denied", codes.PermissionDenied, model.ErrPermissionDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grpcErr := status.Error(tt.code, "test error")
			result := statusToError(grpcErr)
			assert.Equal(t, tt.wantErr, result)
		})
	}

	t.Run("nil error", func(t *testing.T) {
		result := statusToError(nil)
		assert.Nil(t, result)
	})

	t.Run("unknown code", func(t *testing.T) {
		grpcErr := status.Error(codes.Unavailable, "service unavailable")
		result := statusToError(grpcErr)
		assert.Error(t, result)
		assert.Contains(t, result.Error(), "service unavailable")
	})
}

type documentOnlyService struct{ Service }

func TestServer_ExecuteQueryRequiresPageService(t *testing.T) {
	service := new(MockService)
	response, err := NewServer(documentOnlyService{Service: service}).ExecuteQuery(context.Background(), &pb.ExecuteQueryRequest{WireVersion: 2, Database: "database1", Query: &pb.Query{Collection: "users"}})
	require.Equal(t, codes.Unimplemented, status.Code(err))
	assert.Nil(t, response)
	service.AssertNotCalled(t, "ExecuteQuery", mock.Anything, mock.Anything, mock.Anything)
}

func TestServer_ExecuteQueryRejectsUnsupportedVersion(t *testing.T) {
	for _, version := range []uint32{0, 1, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			service := new(MockService)
			response, err := NewServer(service).ExecuteQuery(context.Background(), &pb.ExecuteQueryRequest{WireVersion: version, Database: "database1", Query: &pb.Query{Collection: "users"}})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			assert.Nil(t, response)
			service.AssertNotCalled(t, "ExecuteQueryPage", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestServer_ExecuteQueryErrorDetails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   codes.Code
		reason string
	}{
		{"stale cursor", model.ErrStaleCursor, codes.FailedPrecondition, "STALE_CURSOR"},
		{"work limit", model.ErrQueryWorkLimit, codes.ResourceExhausted, "QUERY_WORK_LIMIT"},
		{"missing index", indexer.ErrNoMatchingIndex, codes.FailedPrecondition, "NO_MATCHING_INDEX"},
		{"index unavailable", indexer.ErrIndexNotReady, codes.Unavailable, "INDEX_UNAVAILABLE"},
		{"index rebuilding", indexer.ErrIndexRebuilding, codes.Unavailable, "INDEX_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := new(MockService)
			service.On("ExecuteQueryPage", mock.Anything, "database1", model.Query{Collection: "users"}).Return(nil, fmt.Errorf("private execution detail: %w", tc.err)).Once()
			response, err := NewServer(service).ExecuteQuery(context.Background(), &pb.ExecuteQueryRequest{Database: "database1", WireVersion: 2, Query: &pb.Query{Collection: "users"}})
			require.Nil(t, response)
			rpcStatus := status.Convert(err)
			require.Equal(t, tc.code, rpcStatus.Code())
			require.Len(t, rpcStatus.Details(), 1)
			detail, ok := rpcStatus.Details()[0].(*errdetails.ErrorInfo)
			require.True(t, ok)
			assert.Equal(t, "syntrix.query", detail.Domain)
			assert.Equal(t, tc.reason, detail.Reason)
			assert.NotContains(t, rpcStatus.Message(), "private execution detail")
			service.AssertExpectations(t)
		})
	}
}

func TestServer_ExecuteQuerySanitizesInternalErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		page model.QueryPage
		err  error
	}{
		{"execution failure", model.QueryPage{}, fmt.Errorf("database password=secret path=/private/storage")},
		{"response encoding failure", model.QueryPage{Documents: []model.Document{{"private-business-field": make(chan int)}}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := new(MockService)
			service.On("ExecuteQueryPage", mock.Anything, "database1", model.Query{Collection: "users"}).Return(tc.page, tc.err).Once()
			response, err := NewServer(service).ExecuteQuery(context.Background(), &pb.ExecuteQueryRequest{Database: "database1", WireVersion: 2, Query: &pb.Query{Collection: "users"}})
			require.Nil(t, response)
			rpcStatus := status.Convert(err)
			require.Equal(t, codes.Internal, rpcStatus.Code())
			assert.Equal(t, "query execution failed", rpcStatus.Message())
			assert.Empty(t, rpcStatus.Details())
			service.AssertExpectations(t)
		})
	}
}

func TestServer_ExecuteQueryRejectsMalformedTypedFilter(t *testing.T) {
	service := new(MockService)
	response, err := NewServer(service).ExecuteQuery(context.Background(), &pb.ExecuteQueryRequest{Database: "database1", WireVersion: 2, Query: &pb.Query{Collection: "users", Filters: []*pb.Filter{{Field: "counter", Op: "==", Value: []byte(`9223372036854775807`)}}}})
	require.Nil(t, response)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	service.AssertNotCalled(t, "ExecuteQueryPage", mock.Anything, mock.Anything, mock.Anything)
}
