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
