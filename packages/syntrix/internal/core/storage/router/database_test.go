package router

import (
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/stretchr/testify/assert"
)

type mockDatabaseDocStore struct {
	types.DocumentStore
}

func TestDatabaseDocumentRouter(t *testing.T) {
	defaultStore := &mockDatabaseDocStore{}
	databaseStore := &mockDatabaseDocStore{}

	defaultRouter := NewSingleDocumentRouter(defaultStore)
	databaseRouter := NewSingleDocumentRouter(databaseStore)

	databases := map[string]types.DocumentRouter{
		"t1": databaseRouter,
	}

	r := NewDatabaseDocumentRouter(defaultRouter, databases)

	// Case 1: Database found
	s, err := r.Select("t1", types.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, databaseStore, s)

	// Case 2: Database not found, use default
	s2, err2 := r.Select("t2", types.OpRead)
	assert.NoError(t, err2)
	assert.Equal(t, defaultStore, s2)

	// Case 3: Database empty -> use default
	s3, err3 := r.Select("", types.OpRead)
	assert.NoError(t, err3)
	assert.Equal(t, defaultStore, s3)

	// Case 4: No default router
	rNoDefault := NewDatabaseDocumentRouter(nil, databases)
	_, err4 := rNoDefault.Select("t2", types.OpRead)
	assert.ErrorIs(t, err4, ErrDatabaseNotFound)

	// Case 5: Database required (no default)
	_, err5 := rNoDefault.Select("", types.OpRead)
	assert.ErrorIs(t, err5, ErrDatabaseRequired)
}
