package router

import (
	"github.com/codetreker/syntrix/internal/identity/repository"
)

// SingleUserRouter routes all operations to a single UserStore
type SingleUserRouter struct {
	store repository.UserStore
}

func NewSingleUserRouter(store repository.UserStore) repository.UserRouter {
	return &SingleUserRouter{store: store}
}

func (r *SingleUserRouter) Select(database string, op repository.OpKind) (repository.UserStore, error) {
	return r.store, nil
}

// SingleRevocationRouter routes all operations to a single TokenRevocationStore
type SingleRevocationRouter struct {
	store repository.TokenRevocationStore
}

func NewSingleRevocationRouter(store repository.TokenRevocationStore) repository.RevocationRouter {
	return &SingleRevocationRouter{store: store}
}

func (r *SingleRevocationRouter) Select(database string, op repository.OpKind) (repository.TokenRevocationStore, error) {
	return r.store, nil
}
