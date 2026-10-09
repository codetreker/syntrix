package scenario

import (
	"context"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
)

// CreateOperation implements the Operation interface for document creation.
type CreateOperation struct {
	collection string
	document   map[string]interface{}
	onCreated  func(id string) // Callback to register created ID
}

// NewCreateOperation creates a new create operation.
func NewCreateOperation(collection string, document map[string]interface{}) *CreateOperation {
	return &CreateOperation{
		collection: collection,
		document:   document,
	}
}

// NewCreateOperationWithCallback creates a new create operation with a callback for ID registration.
func NewCreateOperationWithCallback(collection string, document map[string]interface{}, onCreated func(id string)) *CreateOperation {
	return &CreateOperation{
		collection: collection,
		document:   document,
		onCreated:  onCreated,
	}
}

// Type returns the operation type.
func (o *CreateOperation) Type() string {
	return "create"
}

// Execute performs the create operation.
func (o *CreateOperation) Execute(ctx context.Context, client types.Client) (*types.OperationResult, error) {
	start := time.Now()

	doc, err := client.CreateDocument(ctx, o.collection, o.document)
	duration := time.Since(start)

	result := &types.OperationResult{
		OperationType: o.Type(),
		StartTime:     start,
		Duration:      duration,
		Success:       err == nil,
		Error:         err,
	}

	if doc != nil {
		result.StatusCode = 201
		// Register created ID if callback is set
		if o.onCreated != nil {
			o.onCreated(doc.ID)
		}
	}

	return result, err
}

// ReadOperation implements the Operation interface for document retrieval.
type ReadOperation struct {
	collection string
	id         string
}

// NewReadOperation creates a new read operation.
func NewReadOperation(collection, id string) *ReadOperation {
	return &ReadOperation{
		collection: collection,
		id:         id,
	}
}

// Type returns the operation type.
func (o *ReadOperation) Type() string {
	return "read"
}

// Execute performs the read operation.
func (o *ReadOperation) Execute(ctx context.Context, client types.Client) (*types.OperationResult, error) {
	start := time.Now()

	doc, err := client.GetDocument(ctx, o.collection, o.id)
	duration := time.Since(start)

	result := &types.OperationResult{
		OperationType: o.Type(),
		StartTime:     start,
		Duration:      duration,
		Success:       err == nil,
		Error:         err,
	}

	if doc != nil {
		result.StatusCode = 200
	}

	return result, err
}

// UpdateOperation implements the Operation interface for document updates.
type UpdateOperation struct {
	collection string
	id         string
	document   map[string]interface{}
}

// NewUpdateOperation creates a new update operation.
func NewUpdateOperation(collection, id string, document map[string]interface{}) *UpdateOperation {
	return &UpdateOperation{
		collection: collection,
		id:         id,
		document:   document,
	}
}

// Type returns the operation type.
func (o *UpdateOperation) Type() string {
	return "update"
}

// Execute performs the update operation.
func (o *UpdateOperation) Execute(ctx context.Context, client types.Client) (*types.OperationResult, error) {
	start := time.Now()

	doc, err := client.UpdateDocument(ctx, o.collection, o.id, o.document)
	duration := time.Since(start)

	result := &types.OperationResult{
		OperationType: o.Type(),
		StartTime:     start,
		Duration:      duration,
		Success:       err == nil,
		Error:         err,
	}

	if doc != nil {
		result.StatusCode = 200
	}

	return result, err
}

// DeleteOperation implements the Operation interface for document deletion.
type DeleteOperation struct {
	collection string
	id         string
}

// NewDeleteOperation creates a new delete operation.
func NewDeleteOperation(collection, id string) *DeleteOperation {
	return &DeleteOperation{
		collection: collection,
		id:         id,
	}
}

// Type returns the operation type.
func (o *DeleteOperation) Type() string {
	return "delete"
}

// Execute performs the delete operation.
func (o *DeleteOperation) Execute(ctx context.Context, client types.Client) (*types.OperationResult, error) {
	start := time.Now()

	err := client.DeleteDocument(ctx, o.collection, o.id)
	duration := time.Since(start)

	result := &types.OperationResult{
		OperationType: o.Type(),
		StartTime:     start,
		Duration:      duration,
		Success:       err == nil,
		Error:         err,
	}

	if err == nil {
		result.StatusCode = 204
	}

	return result, err
}
