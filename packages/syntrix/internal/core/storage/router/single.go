package router

import (
	"github.com/codetreker/syntrix/internal/core/storage/types"
)

// SingleRouter routes all operations to a single set of stores.
type SingleRouter struct {
	doc types.DocumentStore
	usr types.UserStore
	rev types.TokenRevocationStore
}

// NewSingleRouter constructs a Router that always returns the provided stores.
func NewSingleRouter(doc types.DocumentStore, usr types.UserStore, rev types.TokenRevocationStore) types.Router {
	return &SingleRouter{doc: doc, usr: usr, rev: rev}
}

// SelectDocument returns the configured DocumentStore for any operation kind.
func (r *SingleRouter) SelectDocument(_ types.OpKind) types.DocumentStore {
	return r.doc
}

// SelectUser returns the configured UserStore for any operation kind.
func (r *SingleRouter) SelectUser(_ types.OpKind) types.UserStore {
	return r.usr
}

// SelectRevocation returns the configured TokenRevocationStore for any operation kind.
func (r *SingleRouter) SelectRevocation(_ types.OpKind) types.TokenRevocationStore {
	return r.rev
}

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

// SingleUserRouter routes all operations to a single UserStore
type SingleUserRouter struct {
	store types.UserStore
}

func NewSingleUserRouter(store types.UserStore) types.UserRouter {
	return &SingleUserRouter{store: store}
}

func (r *SingleUserRouter) Select(database string, op types.OpKind) (types.UserStore, error) {
	return r.store, nil
}

// SingleRevocationRouter routes all operations to a single TokenRevocationStore
type SingleRevocationRouter struct {
	store types.TokenRevocationStore
}

func NewSingleRevocationRouter(store types.TokenRevocationStore) types.RevocationRouter {
	return &SingleRevocationRouter{store: store}
}

func (r *SingleRevocationRouter) Select(database string, op types.OpKind) (types.TokenRevocationStore, error) {
	return r.store, nil
}
