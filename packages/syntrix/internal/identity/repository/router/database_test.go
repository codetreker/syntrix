package router

import (
	"testing"

	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/stretchr/testify/assert"
)

type mockDatabaseUserStore struct {
	repository.UserStore
}

type mockDatabaseRevStore struct {
	repository.TokenRevocationStore
}

func TestDatabaseUserRouter(t *testing.T) {
	defaultStore := &mockDatabaseUserStore{}
	databaseStore := &mockDatabaseUserStore{}

	defaultRouter := NewSingleUserRouter(defaultStore)
	databaseRouter := NewSingleUserRouter(databaseStore)

	databases := map[string]repository.UserRouter{
		"t1": databaseRouter,
	}

	r := NewDatabaseUserRouter(defaultRouter, databases)

	// Case 1: Database found
	s, err := r.Select("t1", repository.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, databaseStore, s)

	// Case 2: Database not found, use default
	s2, err2 := r.Select("t2", repository.OpRead)
	assert.NoError(t, err2)
	assert.Equal(t, defaultStore, s2)

	// Case 3: Database required
	_, err3 := r.Select("", repository.OpRead)
	assert.ErrorIs(t, err3, ErrDatabaseRequired)

	// Case 4: No default router
	rNoDefault := NewDatabaseUserRouter(nil, databases)
	_, err4 := rNoDefault.Select("t2", repository.OpRead)
	assert.ErrorIs(t, err4, ErrDatabaseNotFound)
}

func TestDatabaseRevocationRouter(t *testing.T) {
	defaultStore := &mockDatabaseRevStore{}
	databaseStore := &mockDatabaseRevStore{}

	defaultRouter := NewSingleRevocationRouter(defaultStore)
	databaseRouter := NewSingleRevocationRouter(databaseStore)

	databases := map[string]repository.RevocationRouter{
		"t1": databaseRouter,
	}

	r := NewDatabaseRevocationRouter(defaultRouter, databases)

	// Case 1: Database found
	s, err := r.Select("t1", repository.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, databaseStore, s)

	// Case 2: Database not found, use default
	s2, err2 := r.Select("t2", repository.OpRead)
	assert.NoError(t, err2)
	assert.Equal(t, defaultStore, s2)

	// Case 3: Database required
	_, err3 := r.Select("", repository.OpRead)
	assert.ErrorIs(t, err3, ErrDatabaseRequired)

	// Case 4: No default router
	rNoDefault := NewDatabaseRevocationRouter(nil, databases)
	_, err4 := rNoDefault.Select("t2", repository.OpRead)
	assert.ErrorIs(t, err4, ErrDatabaseNotFound)
}
