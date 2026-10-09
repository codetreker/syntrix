package router

import (
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/stretchr/testify/assert"
)

type mockDocStore struct {
	types.DocumentStore
	id string
}

type mockUserStore struct {
	types.UserStore
	id string
}

type mockRevStore struct {
	types.TokenRevocationStore
	id string
}

func TestSplitDocumentRouter(t *testing.T) {
	primary := &mockDocStore{id: "primary"}
	replica := &mockDocStore{id: "replica"}
	router := NewSplitDocumentRouter(primary, replica)

	s, err := router.Select("default", types.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, replica, s)

	s, err = router.Select("default", types.OpWrite)
	assert.NoError(t, err)
	assert.Equal(t, primary, s)

	s, err = router.Select("default", types.OpWatch)
	assert.NoError(t, err)
	assert.Same(t, primary, s)
}

func TestSplitUserRouter(t *testing.T) {
	primary := &mockUserStore{id: "primary"}
	replica := &mockUserStore{id: "replica"}
	router := NewSplitUserRouter(primary, replica)

	s, err := router.Select("default", types.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, replica, s)

	s, err = router.Select("default", types.OpWrite)
	assert.NoError(t, err)
	assert.Equal(t, primary, s)
}

func TestSplitRevocationRouter(t *testing.T) {
	primary := &mockRevStore{id: "primary"}
	replica := &mockRevStore{id: "replica"}
	router := NewSplitRevocationRouter(primary, replica)

	s, err := router.Select("default", types.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, replica, s)

	s, err = router.Select("default", types.OpWrite)
	assert.NoError(t, err)
	assert.Equal(t, primary, s)
}
