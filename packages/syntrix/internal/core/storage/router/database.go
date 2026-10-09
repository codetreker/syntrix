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

// DatabaseUserRouter routes operations based on database
type DatabaseUserRouter struct {
	defaultRouter types.UserRouter
	databases     map[string]types.UserRouter
}

func NewDatabaseUserRouter(defaultRouter types.UserRouter, databases map[string]types.UserRouter) types.UserRouter {
	return &DatabaseUserRouter{
		defaultRouter: defaultRouter,
		databases:     databases,
	}
}

func (r *DatabaseUserRouter) Select(database string, op types.OpKind) (types.UserStore, error) {
	if database == "" {
		return nil, ErrDatabaseRequired
	}

	router, ok := r.databases[database]
	if !ok {
		router = r.defaultRouter
	}

	if router == nil {
		return nil, ErrDatabaseNotFound
	}

	return router.Select(database, op)
}

// DatabaseRevocationRouter routes operations based on database
type DatabaseRevocationRouter struct {
	defaultRouter types.RevocationRouter
	databases     map[string]types.RevocationRouter
}

func NewDatabaseRevocationRouter(defaultRouter types.RevocationRouter, databases map[string]types.RevocationRouter) types.RevocationRouter {
	return &DatabaseRevocationRouter{
		defaultRouter: defaultRouter,
		databases:     databases,
	}
}

func (r *DatabaseRevocationRouter) Select(database string, op types.OpKind) (types.TokenRevocationStore, error) {
	if database == "" {
		return nil, ErrDatabaseRequired
	}

	router, ok := r.databases[database]
	if !ok {
		router = r.defaultRouter
	}

	if router == nil {
		return nil, ErrDatabaseNotFound
	}

	return router.Select(database, op)
}
