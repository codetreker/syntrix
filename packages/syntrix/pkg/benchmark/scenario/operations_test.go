package scenario

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock client for testing
type mockClient struct {
	createErr bool
	readErr   bool
	updateErr bool
	deleteErr bool
}

func (m *mockClient) CreateDocument(ctx context.Context, collection string, doc map[string]interface{}) (*types.Document, error) {
	if m.createErr {
		return nil, errors.New("create error")
	}
	return &types.Document{ID: "doc-123", Data: doc}, nil
}

func (m *mockClient) GetDocument(ctx context.Context, collection, id string) (*types.Document, error) {
	if m.readErr {
		return nil, errors.New("read error")
	}
	return &types.Document{ID: id, Data: map[string]interface{}{"test": "data"}}, nil
}

func (m *mockClient) UpdateDocument(ctx context.Context, collection, id string, doc map[string]interface{}) (*types.Document, error) {
	if m.updateErr {
		return nil, errors.New("update error")
	}
	return &types.Document{ID: id, Data: doc}, nil
}

func (m *mockClient) DeleteDocument(ctx context.Context, collection, id string) error {
	if m.deleteErr {
		return errors.New("delete error")
	}
	return nil
}

func (m *mockClient) Query(ctx context.Context, query types.Query) ([]*types.Document, error) {
	return []*types.Document{{ID: "doc-1"}}, nil
}

func (m *mockClient) Subscribe(ctx context.Context, query types.Query) (*types.Subscription, error) {
	return &types.Subscription{ID: "sub-1"}, nil
}

func (m *mockClient) Unsubscribe(ctx context.Context, subID string) error {
	return nil
}

func (m *mockClient) Close() error {
	return nil
}

// Tests for CreateOperation

func TestNewCreateOperation(t *testing.T) {
	doc := map[string]interface{}{"field": "value"}
	op := NewCreateOperation("test-collection", doc)

	assert.NotNil(t, op)
	assert.Equal(t, "test-collection", op.collection)
	assert.Equal(t, doc, op.document)
}

func TestCreateOperation_Type(t *testing.T) {
	op := NewCreateOperation("test", nil)
	assert.Equal(t, "create", op.Type())
}

func TestCreateOperation_Execute(t *testing.T) {
	t.Run("successful create", func(t *testing.T) {
		client := &mockClient{}
		doc := map[string]interface{}{"field": "value"}
		op := NewCreateOperation("test-collection", doc)

		result, err := op.Execute(context.Background(), client)

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "create", result.OperationType)
		assert.True(t, result.Success)
		assert.Equal(t, 201, result.StatusCode)
		assert.GreaterOrEqual(t, result.Duration, time.Duration(0))
	})

	t.Run("failed create", func(t *testing.T) {
		client := &mockClient{createErr: true}
		op := NewCreateOperation("test-collection", map[string]interface{}{})

		result, err := op.Execute(context.Background(), client)

		assert.Error(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "create", result.OperationType)
		assert.False(t, result.Success)
		assert.NotNil(t, result.Error)
	})
}

// Tests for ReadOperation

func TestNewReadOperation(t *testing.T) {
	op := NewReadOperation("test-collection", "doc-123")

	assert.NotNil(t, op)
	assert.Equal(t, "test-collection", op.collection)
	assert.Equal(t, "doc-123", op.id)
}

func TestReadOperation_Type(t *testing.T) {
	op := NewReadOperation("test", "id")
	assert.Equal(t, "read", op.Type())
}

func TestReadOperation_Execute(t *testing.T) {
	t.Run("successful read", func(t *testing.T) {
		client := &mockClient{}
		op := NewReadOperation("test-collection", "doc-123")

		result, err := op.Execute(context.Background(), client)

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "read", result.OperationType)
		assert.True(t, result.Success)
		assert.Equal(t, 200, result.StatusCode)
		assert.GreaterOrEqual(t, result.Duration, time.Duration(0))
	})

	t.Run("failed read", func(t *testing.T) {
		client := &mockClient{readErr: true}
		op := NewReadOperation("test-collection", "doc-123")

		result, err := op.Execute(context.Background(), client)

		assert.Error(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "read", result.OperationType)
		assert.False(t, result.Success)
		assert.NotNil(t, result.Error)
	})
}

// Tests for UpdateOperation

func TestNewUpdateOperation(t *testing.T) {
	doc := map[string]interface{}{"updated": "value"}
	op := NewUpdateOperation("test-collection", "doc-123", doc)

	assert.NotNil(t, op)
	assert.Equal(t, "test-collection", op.collection)
	assert.Equal(t, "doc-123", op.id)
	assert.Equal(t, doc, op.document)
}

func TestUpdateOperation_Type(t *testing.T) {
	op := NewUpdateOperation("test", "id", nil)
	assert.Equal(t, "update", op.Type())
}

func TestUpdateOperation_Execute(t *testing.T) {
	t.Run("successful update", func(t *testing.T) {
		client := &mockClient{}
		doc := map[string]interface{}{"updated": "value"}
		op := NewUpdateOperation("test-collection", "doc-123", doc)

		result, err := op.Execute(context.Background(), client)

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "update", result.OperationType)
		assert.True(t, result.Success)
		assert.Equal(t, 200, result.StatusCode)
		assert.GreaterOrEqual(t, result.Duration, time.Duration(0))
	})

	t.Run("failed update", func(t *testing.T) {
		client := &mockClient{updateErr: true}
		op := NewUpdateOperation("test-collection", "doc-123", map[string]interface{}{})

		result, err := op.Execute(context.Background(), client)

		assert.Error(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "update", result.OperationType)
		assert.False(t, result.Success)
		assert.NotNil(t, result.Error)
	})
}

// Tests for DeleteOperation

func TestNewDeleteOperation(t *testing.T) {
	op := NewDeleteOperation("test-collection", "doc-123")

	assert.NotNil(t, op)
	assert.Equal(t, "test-collection", op.collection)
	assert.Equal(t, "doc-123", op.id)
}

func TestDeleteOperation_Type(t *testing.T) {
	op := NewDeleteOperation("test", "id")
	assert.Equal(t, "delete", op.Type())
}

func TestDeleteOperation_Execute(t *testing.T) {
	t.Run("successful delete", func(t *testing.T) {
		client := &mockClient{}
		op := NewDeleteOperation("test-collection", "doc-123")

		result, err := op.Execute(context.Background(), client)

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "delete", result.OperationType)
		assert.True(t, result.Success)
		assert.Equal(t, 204, result.StatusCode)
		assert.GreaterOrEqual(t, result.Duration, time.Duration(0))
	})

	t.Run("failed delete", func(t *testing.T) {
		client := &mockClient{deleteErr: true}
		op := NewDeleteOperation("test-collection", "doc-123")

		result, err := op.Execute(context.Background(), client)

		assert.Error(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "delete", result.OperationType)
		assert.False(t, result.Success)
		assert.NotNil(t, result.Error)
	})
}
