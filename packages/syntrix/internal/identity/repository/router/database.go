package router

import (
	"errors"

	"github.com/codetreker/syntrix/internal/identity/repository"
)

var (
	ErrDatabaseNotFound = errors.New("database not found")
	ErrDatabaseRequired = errors.New("database is required")
)

// DatabaseUserRouter routes operations based on database
type DatabaseUserRouter struct {
	defaultRouter repository.UserRouter
	databases     map[string]repository.UserRouter
}

func NewDatabaseUserRouter(defaultRouter repository.UserRouter, databases map[string]repository.UserRouter) repository.UserRouter {
	return &DatabaseUserRouter{
		defaultRouter: defaultRouter,
		databases:     databases,
	}
}

func (r *DatabaseUserRouter) Select(database string, op repository.OpKind) (repository.UserStore, error) {
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
	defaultRouter repository.RevocationRouter
	databases     map[string]repository.RevocationRouter
}

func NewDatabaseRevocationRouter(defaultRouter repository.RevocationRouter, databases map[string]repository.RevocationRouter) repository.RevocationRouter {
	return &DatabaseRevocationRouter{
		defaultRouter: defaultRouter,
		databases:     databases,
	}
}

func (r *DatabaseRevocationRouter) Select(database string, op repository.OpKind) (repository.TokenRevocationStore, error) {
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
