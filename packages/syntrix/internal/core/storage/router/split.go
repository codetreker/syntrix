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
