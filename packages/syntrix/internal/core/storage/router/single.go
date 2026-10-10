package router

import (
	"github.com/codetreker/syntrix/internal/core/storage/types"
)

// SingleDocumentRouter routes all operations to a single DocumentStore
type SingleDocumentRouter struct {
	store types.DocumentStore
}

func NewSingleDocumentRouter(store types.DocumentStore) types.DocumentRouter {
	return &SingleDocumentRouter{store: store}
}

func (r *SingleDocumentRouter) Select(database string, op types.OpKind) (types.DocumentStore, error) {
	return r.store, nil
}
