package router

import (
	"github.com/codetreker/syntrix/internal/identity/repository"
)

// SplitUserRouter routes read operations to replica and write operations to primary
type SplitUserRouter struct {
	primary repository.UserStore
	replica repository.UserStore
}

func NewSplitUserRouter(primary, replica repository.UserStore) repository.UserRouter {
	return &SplitUserRouter{primary: primary, replica: replica}
}

func (r *SplitUserRouter) Select(database string, op repository.OpKind) (repository.UserStore, error) {
	if op == repository.OpRead {
		return r.replica, nil
	}
	return r.primary, nil
}

// SplitRevocationRouter routes read operations to replica and write operations to primary
type SplitRevocationRouter struct {
	primary repository.TokenRevocationStore
	replica repository.TokenRevocationStore
}

func NewSplitRevocationRouter(primary, replica repository.TokenRevocationStore) repository.RevocationRouter {
	return &SplitRevocationRouter{primary: primary, replica: replica}
}

func (r *SplitRevocationRouter) Select(database string, op repository.OpKind) (repository.TokenRevocationStore, error) {
	if op == repository.OpRead {
		return r.replica, nil
	}
	return r.primary, nil
}
