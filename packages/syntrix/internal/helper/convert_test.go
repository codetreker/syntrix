package helper

import (
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/stretchr/testify/assert"
)

func TestFlattenStorageDocument_Nil(t *testing.T) {
	res := FlattenStorageDocument(nil)
	assert.Nil(t, res)
}

func TestFlattenStorageDocument_Deleted(t *testing.T) {
	doc := &types.StoredDoc{
		Fullpath:   "col/doc1",
		Collection: "col",
		Data:       map[string]interface{}{"foo": "bar"},
		Deleted:    true,
	}
	res := FlattenStorageDocument(doc)
	assert.True(t, res["deleted"].(bool))
}

func TestExtractIDFromFullpath_Invalid(t *testing.T) {
	id := extractIDFromFullpath("col")
	assert.Equal(t, "", id)
}

func TestExtractIDFromFullpath_Valid(t *testing.T) {
	id := extractIDFromFullpath("col/doc1")
	assert.Equal(t, "doc1", id)
}
