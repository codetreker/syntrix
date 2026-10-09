package router

import (
	"github.com/codetreker/syntrix/internal/core/storage/types"
)

// SplitDocumentRouter uses the replica for ordinary reads. Watches use the
// primary so a checkpoint stays bound to the authoritative change source.
type SplitDocumentRouter struct {
	primary types.DocumentStore
	replica types.DocumentStore
}

func NewSplitDocumentRouter(primary, replica types.DocumentStore) types.DocumentRouter {
	return &SplitDocumentRouter{primary: primary, replica: replica}
}

func (r *SplitDocumentRouter) Select(database string, op types.OpKind) (types.DocumentStore, error) {
	if op == types.OpRead {
		return r.replica, nil
	}
	return r.primary, nil
}

// SplitUserRouter routes read operations to replica and write operations to primary
type SplitUserRouter struct {
	primary types.UserStore
	replica types.UserStore
}

func NewSplitUserRouter(primary, replica types.UserStore) types.UserRouter {
	return &SplitUserRouter{primary: primary, replica: replica}
}

func (r *SplitUserRouter) Select(database string, op types.OpKind) (types.UserStore, error) {
	if op == types.OpRead {
		return r.replica, nil
	}
	return r.primary, nil
}

// SplitRevocationRouter routes read operations to replica and write operations to primary
type SplitRevocationRouter struct {
	primary types.TokenRevocationStore
	replica types.TokenRevocationStore
}

func NewSplitRevocationRouter(primary, replica types.TokenRevocationStore) types.RevocationRouter {
	return &SplitRevocationRouter{primary: primary, replica: replica}
}

func (r *SplitRevocationRouter) Select(database string, op types.OpKind) (types.TokenRevocationStore, error) {
	if op == types.OpRead {
		return r.replica, nil
	}
	return r.primary, nil
}
