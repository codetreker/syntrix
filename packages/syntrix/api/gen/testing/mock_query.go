// Package testing provides mock implementations of gRPC service clients for testing.
package testing

import (
	"context"

	queryv1 "github.com/codetreker/syntrix/api/gen/query/v1"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
)

// MockQueryServiceClient is a mock implementation of queryv1.QueryServiceClient
// using testify/mock for easy test setup and assertions.
type MockQueryServiceClient struct {
	mock.Mock
}

// NewMockQueryServiceClient creates a new MockQueryServiceClient.
func NewMockQueryServiceClient() *MockQueryServiceClient {
	return &MockQueryServiceClient{}
}

// GetDocument implements queryv1.QueryServiceClient.
func (m *MockQueryServiceClient) GetDocument(ctx context.Context, req *queryv1.GetDocumentRequest, opts ...grpc.CallOption) (*queryv1.GetDocumentResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*queryv1.GetDocumentResponse), args.Error(1)
}

// CreateDocument implements queryv1.QueryServiceClient.
func (m *MockQueryServiceClient) CreateDocument(ctx context.Context, req *queryv1.CreateDocumentRequest, opts ...grpc.CallOption) (*queryv1.CreateDocumentResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*queryv1.CreateDocumentResponse), args.Error(1)
}

// ReplaceDocument implements queryv1.QueryServiceClient.
func (m *MockQueryServiceClient) ReplaceDocument(ctx context.Context, req *queryv1.ReplaceDocumentRequest, opts ...grpc.CallOption) (*queryv1.ReplaceDocumentResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*queryv1.ReplaceDocumentResponse), args.Error(1)
}

// PatchDocument implements queryv1.QueryServiceClient.
func (m *MockQueryServiceClient) PatchDocument(ctx context.Context, req *queryv1.PatchDocumentRequest, opts ...grpc.CallOption) (*queryv1.PatchDocumentResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*queryv1.PatchDocumentResponse), args.Error(1)
}

// DeleteDocument implements queryv1.QueryServiceClient.
func (m *MockQueryServiceClient) DeleteDocument(ctx context.Context, req *queryv1.DeleteDocumentRequest, opts ...grpc.CallOption) (*queryv1.DeleteDocumentResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*queryv1.DeleteDocumentResponse), args.Error(1)
}

// ExecuteQuery implements queryv1.QueryServiceClient.
func (m *MockQueryServiceClient) ExecuteQuery(ctx context.Context, req *queryv1.ExecuteQueryRequest, opts ...grpc.CallOption) (*queryv1.ExecuteQueryResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*queryv1.ExecuteQueryResponse), args.Error(1)
}

// Pull implements queryv1.QueryServiceClient.
func (m *MockQueryServiceClient) Pull(ctx context.Context, req *queryv1.PullRequest, opts ...grpc.CallOption) (*queryv1.PullResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*queryv1.PullResponse), args.Error(1)
}

// Push implements queryv1.QueryServiceClient.
func (m *MockQueryServiceClient) Push(ctx context.Context, req *queryv1.PushRequest, opts ...grpc.CallOption) (*queryv1.PushResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*queryv1.PushResponse), args.Error(1)
}

// Compile-time check that MockQueryServiceClient implements queryv1.QueryServiceClient.
var _ queryv1.QueryServiceClient = (*MockQueryServiceClient)(nil)
