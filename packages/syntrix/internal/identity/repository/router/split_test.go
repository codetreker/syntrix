package router

import (
	"testing"

	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/stretchr/testify/assert"
)

type mockUserStore struct {
	repository.UserStore
	id string
}

type mockRevStore struct {
	repository.TokenRevocationStore
	id string
}

func TestSplitUserRouter(t *testing.T) {
	primary := &mockUserStore{id: "primary"}
	replica := &mockUserStore{id: "replica"}
	router := NewSplitUserRouter(primary, replica)

	s, err := router.Select("default", repository.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, replica, s)

	s, err = router.Select("default", repository.OpWrite)
	assert.NoError(t, err)
	assert.Equal(t, primary, s)
}

func TestSplitRevocationRouter(t *testing.T) {
	primary := &mockRevStore{id: "primary"}
	replica := &mockRevStore{id: "replica"}
	router := NewSplitRevocationRouter(primary, replica)

	s, err := router.Select("default", repository.OpRead)
	assert.NoError(t, err)
	assert.Equal(t, replica, s)

	s, err = router.Select("default", repository.OpWrite)
	assert.NoError(t, err)
	assert.Equal(t, primary, s)
}
