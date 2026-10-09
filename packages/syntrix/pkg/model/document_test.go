package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckDocumentID(t *testing.T) {
	assert.True(t, CheckDocumentID("abc-123_.$"[:8]))
	assert.False(t, CheckDocumentID(""))
	assert.False(t, CheckDocumentID("with space"))
	longID := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.extra"
	assert.False(t, CheckDocumentID(longID))
}

func TestGenerateIDIfEmpty(t *testing.T) {
	doc := Document{"collection": "users"}
	doc.GenerateIDIfEmpty()
	assert.NotEmpty(t, doc.GetID())

	existing := Document{"id": "fixed", "collection": "users"}
	existing.GenerateIDIfEmpty()
	assert.Equal(t, "fixed", existing.GetID())
}

func TestSettersAndGetters(t *testing.T) {
	doc := Document{}

	doc.SetID("abc")
	assert.Equal(t, "abc", doc.GetID())

	doc.SetCollection("col")
	assert.Equal(t, "col", doc.GetCollection())

	// Test invalid types for GetID and GetCollection
	invalidDoc := Document{
		"id":         123,
		"collection": 123,
	}
	assert.Equal(t, "", invalidDoc.GetID())
	assert.Equal(t, "", invalidDoc.GetCollection())

	assert.False(t, doc.HasVersion())
	assert.Equal(t, int64(-1), doc.GetVersion())

	doc["version"] = float64(3)
	assert.True(t, doc.HasVersion())
	assert.Equal(t, int64(3), doc.GetVersion())

	assert.True(t, doc.HasKey("version"))
}

func TestValidateDocument(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		var doc Document
		err := doc.ValidateDocument()
		assert.Error(t, err)
	})

	t.Run("invalid id type", func(t *testing.T) {
		doc := Document{"id": struct{}{}, "collection": "c"}
		err := doc.ValidateDocument()
		assert.Error(t, err)
	})

	t.Run("invalid id format", func(t *testing.T) {
		doc := Document{"id": "bad space", "collection": "c"}
		err := doc.ValidateDocument()
		assert.Error(t, err)
	})

	t.Run("int id converted", func(t *testing.T) {
		doc := Document{"id": int64(5), "collection": "c", "version": int64(1), "updatedAt": int64(2), "createdAt": int64(3), "deleted": true}
		err := doc.ValidateDocument()
		assert.NoError(t, err)
		assert.Equal(t, "5", doc.GetID())

		// protected fields stripped
		doc.StripProtectedFields()
		assert.False(t, doc.HasKey("version"))
		assert.False(t, doc.HasKey("updatedAt"))
		assert.False(t, doc.HasKey("createdAt"))
		assert.False(t, doc.HasKey("collection"))
		assert.False(t, doc.HasKey("deleted"))
	})

	t.Run("empty id string", func(t *testing.T) {
		doc := Document{"id": ""}
		err := doc.ValidateDocument()
		assert.Error(t, err)
	})
}

func TestStripProtectedFieldsAndIsEmpty(t *testing.T) {
	doc := Document{"id": "1", "version": int64(2), "updatedAt": int64(3), "createdAt": int64(4), "collection": "c", "foo": "bar"}
	doc.StripProtectedFields()
	assert.False(t, doc.HasKey("version"))
	assert.False(t, doc.HasKey("updatedAt"))
	assert.False(t, doc.HasKey("createdAt"))
	assert.False(t, doc.HasKey("collection"))
	assert.Equal(t, "bar", doc["foo"])

	empty := Document{"id": "1"}
	assert.True(t, empty.IsEmpty())

	nonEmpty := Document{"id": "1", "x": 1}
	assert.False(t, nonEmpty.IsEmpty())
}

func TestIsDeleted(t *testing.T) {
	t.Run("deleted true", func(t *testing.T) {
		doc := Document{"id": "1", "deleted": true}
		assert.True(t, doc.IsDeleted())
	})

	t.Run("deleted false", func(t *testing.T) {
		doc := Document{"id": "1", "deleted": false}
		assert.False(t, doc.IsDeleted())
	})

	t.Run("deleted missing", func(t *testing.T) {
		doc := Document{"id": "1"}
		assert.False(t, doc.IsDeleted())
	})

	t.Run("deleted wrong type", func(t *testing.T) {
		doc := Document{"id": "1", "deleted": "true"}
		assert.False(t, doc.IsDeleted())
	})
}
