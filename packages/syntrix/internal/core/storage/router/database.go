package router

import (
	"errors"

	"github.com/codetreker/syntrix/internal/core/storage/types"
)

var (
	ErrDatabaseNotFound = errors.New("database not found")
	ErrDatabaseRequired = errors.New("database is required")
)

// DatabaseDocumentRouter routes operations based on database
type DatabaseDocumentRouter struct {
	defaultRouter types.DocumentRouter
	databases     map[string]types.DocumentRouter
}

func NewDatabaseDocumentRouter(defaultRouter types.DocumentRouter, databases map[string]types.DocumentRouter) types.DocumentRouter {
	return &DatabaseDocumentRouter{
		defaultRouter: defaultRouter,
		databases:     databases,
	}
}

func (r *DatabaseDocumentRouter) Select(database string, op types.OpKind) (types.DocumentStore, error) {
	router, ok := r.databases[database]
	if !ok {
		router = r.defaultRouter
	}

	if router == nil {
		if database == "" {
			return nil, ErrDatabaseRequired
		}
		return nil, ErrDatabaseNotFound
	}

	return router.Select(database, op)
}
