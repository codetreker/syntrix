package rest

import (
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/stretchr/testify/assert"
)

func TestFlattenDocument(t *testing.T) {
	t.Run("Nil Document", func(t *testing.T) {
		res := flattenDocument(nil)
		assert.Nil(t, res)
	})

	t.Run("Deleted Document", func(t *testing.T) {
		doc := &storage.StoredDoc{
			Fullpath:   "test/1",
			Collection: "test",
			Data:       map[string]interface{}{"foo": "bar"},
			Deleted:    true,
		}
		res := flattenDocument(doc)
		assert.NotNil(t, res)
		assert.True(t, res["deleted"].(bool))
		assert.Equal(t, "1", res["id"])
	})

	t.Run("Normal Document", func(t *testing.T) {
		doc := &storage.StoredDoc{
			Fullpath:   "test/1",
			Collection: "test",
			Data:       map[string]interface{}{"foo": "bar"},
			Version:    1,
			UpdatedAt:  100,
			CreatedAt:  100,
		}
		res := flattenDocument(doc)
		assert.NotNil(t, res)
		assert.Nil(t, res["deleted"])
		assert.Equal(t, "1", res["id"])
		assert.Equal(t, int64(1), res["version"])
	})

	t.Run("ID in Data", func(t *testing.T) {
		doc := &storage.StoredDoc{
			Fullpath:   "test/1",
			Collection: "test",
			Data:       map[string]interface{}{"id": "custom_id"},
		}
		res := flattenDocument(doc)
		assert.Equal(t, "custom_id", res["id"])
	})
}
