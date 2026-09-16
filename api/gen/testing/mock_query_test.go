package testing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	queryv1 "github.com/syntrixbase/syntrix/api/gen/query/v1"
)

func TestMockQueryServiceClient_GetDocument(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("returns configured response", func(t *testing.T) {
		mockClient := NewMockQueryServiceClient()
		mockClient.On("GetDocument", mock.Anything, mock.Anything).Return(&queryv1.GetDocumentResponse{
			Document: &queryv1.Document{
				Id:         "user1",
				Collection: "users",
			},
		}, nil)

		resp, err := mockClient.GetDocument(ctx, &queryv1.GetDocumentRequest{
			Database: "db1",
			Path:     "users/user1",
		})

		require.NoError(t, err)
		assert.Equal(t, "user1", resp.Document.Id)
		assert.Equal(t, "users", resp.Document.Collection)
		mockClient.AssertExpectations(t)
	})

	t.Run("returns error", func(t *testing.T) {
		mockClient := NewMockQueryServiceClient()
		expectedErr := errors.New("connection failed")
		mockClient.On("GetDocument", mock.Anything, mock.Anything).Return(nil, expectedErr)

		_, err := mockClient.GetDocument(ctx, &queryv1.GetDocumentRequest{})

		assert.ErrorIs(t, err, expectedErr)
		mockClient.AssertExpectations(t)
	})

	t.Run("with specific request matching", func(t *testing.T) {
		mockClient := NewMockQueryServiceClient()
		mockClient.On("GetDocument", mock.Anything, mock.MatchedBy(func(req *queryv1.GetDocumentRequest) bool {
			return req.Database == "db1" && req.Path == "users/alice"
		})).Return(&queryv1.GetDocumentResponse{
			Document: &queryv1.Document{Id: "alice"},
		}, nil)

		resp, err := mockClient.GetDocument(ctx, &queryv1.GetDocumentRequest{
			Database: "db1",
			Path:     "users/alice",
		})

		require.NoError(t, err)
		assert.Equal(t, "alice", resp.Document.Id)
		mockClient.AssertExpectations(t)
	})
}

func TestMockQueryServiceClient_CreateDocument(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("returns success", func(t *testing.T) {
		mockClient := NewMockQueryServiceClient()
		mockClient.On("CreateDocument", mock.Anything, mock.Anything).Return(&queryv1.CreateDocumentResponse{}, nil)

		resp, err := mockClient.CreateDocument(ctx, &queryv1.CreateDocumentRequest{
			Database: "db1",
			Document: &queryv1.Document{Id: "doc1"},
		})

		require.NoError(t, err)
		assert.NotNil(t, resp)
		mockClient.AssertExpectations(t)
	})
}

func TestMockQueryServiceClient_ExecuteQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("returns multiple documents", func(t *testing.T) {
		mockClient := NewMockQueryServiceClient()
		mockClient.On("ExecuteQuery", mock.Anything, mock.Anything).Return(&queryv1.ExecuteQueryResponse{
			Documents: []*queryv1.Document{
				{Id: "doc1"},
				{Id: "doc2"},
			},
		}, nil)

		resp, err := mockClient.ExecuteQuery(ctx, &queryv1.ExecuteQueryRequest{
			Database: "db1",
			Query:    &queryv1.Query{Collection: "users"},
		})

		require.NoError(t, err)
		assert.Len(t, resp.Documents, 2)
		mockClient.AssertExpectations(t)
	})
}

func TestMockQueryServiceClient_AllMethods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mockClient := NewMockQueryServiceClient()

	// Setup all methods
	mockClient.On("GetDocument", mock.Anything, mock.Anything).Return(&queryv1.GetDocumentResponse{}, nil)
	mockClient.On("CreateDocument", mock.Anything, mock.Anything).Return(&queryv1.CreateDocumentResponse{}, nil)
	mockClient.On("ReplaceDocument", mock.Anything, mock.Anything).Return(&queryv1.ReplaceDocumentResponse{}, nil)
	mockClient.On("PatchDocument", mock.Anything, mock.Anything).Return(&queryv1.PatchDocumentResponse{}, nil)
	mockClient.On("DeleteDocument", mock.Anything, mock.Anything).Return(&queryv1.DeleteDocumentResponse{}, nil)
	mockClient.On("ExecuteQuery", mock.Anything, mock.Anything).Return(&queryv1.ExecuteQueryResponse{}, nil)
	mockClient.On("Pull", mock.Anything, mock.Anything).Return(&queryv1.PullResponse{}, nil)
	mockClient.On("Push", mock.Anything, mock.Anything).Return(&queryv1.PushResponse{}, nil)

	// Call all methods
	mockClient.GetDocument(ctx, &queryv1.GetDocumentRequest{})
	mockClient.CreateDocument(ctx, &queryv1.CreateDocumentRequest{})
	mockClient.ReplaceDocument(ctx, &queryv1.ReplaceDocumentRequest{})
	mockClient.PatchDocument(ctx, &queryv1.PatchDocumentRequest{})
	mockClient.DeleteDocument(ctx, &queryv1.DeleteDocumentRequest{})
	mockClient.ExecuteQuery(ctx, &queryv1.ExecuteQueryRequest{})
	mockClient.Pull(ctx, &queryv1.PullRequest{})
	mockClient.Push(ctx, &queryv1.PushRequest{})

	mockClient.AssertExpectations(t)
}

func TestMockQueryServiceClient_PullAndPush(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("Pull with custom response", func(t *testing.T) {
		mockClient := NewMockQueryServiceClient()
		mockClient.On("Pull", mock.Anything, mock.Anything).Return(&queryv1.PullResponse{
			Documents:  []*queryv1.Document{{Id: "doc1"}},
			Checkpoint: "source-position",
		}, nil)

		resp, err := mockClient.Pull(ctx, &queryv1.PullRequest{
			Database:   "db1",
			Collection: "users",
		})

		require.NoError(t, err)
		assert.Len(t, resp.Documents, 1)
		assert.Equal(t, "source-position", resp.Checkpoint)
		mockClient.AssertExpectations(t)
	})

	t.Run("Push with conflicts", func(t *testing.T) {
		mockClient := NewMockQueryServiceClient()
		mockClient.On("Push", mock.Anything, mock.Anything).Return(&queryv1.PushResponse{
			Conflicts: []*queryv1.Document{{Id: "conflict1"}},
		}, nil)

		resp, err := mockClient.Push(ctx, &queryv1.PushRequest{
			Database:   "db1",
			Collection: "users",
		})

		require.NoError(t, err)
		assert.Len(t, resp.Conflicts, 1)
		mockClient.AssertExpectations(t)
	})
}

func TestMockQueryServiceClient_CallCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mockClient := NewMockQueryServiceClient()
	mockClient.On("GetDocument", mock.Anything, mock.Anything).Return(&queryv1.GetDocumentResponse{}, nil)

	// Call multiple times
	mockClient.GetDocument(ctx, &queryv1.GetDocumentRequest{})
	mockClient.GetDocument(ctx, &queryv1.GetDocumentRequest{})
	mockClient.GetDocument(ctx, &queryv1.GetDocumentRequest{})

	// Verify call count using testify/mock
	mockClient.AssertNumberOfCalls(t, "GetDocument", 3)
}
