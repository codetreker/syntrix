package indexer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
)

func TestDocumentFieldEnvelopePrecedenceAndTombstones(t *testing.T) {
	doc := types.StoredDoc{Id: "internal-storage-hash", Database: "db", Collection: "items", Fullpath: "items/logical-id", Version: 9007199254740993, CreatedAt: 123, UpdatedAt: 456,
		Data: map[string]any{"id": "shadow-id", "collection": "shadow-collection", "version": -1, "createdAt": -1, "updatedAt": -1, "deleted": "shadow-deleted", "business": "value", "explicitNull": nil}}
	for _, deleted := range []bool{false, true} {
		doc.Deleted = deleted
		for field, want := range map[string]any{"id": "logical-id", "collection": "items", "version": int64(9007199254740993), "createdAt": int64(123), "updatedAt": int64(456), "deleted": deleted} {
			got, present := DocumentField(&doc, "logical-id", field)
			assert.True(t, present, field)
			assert.Equal(t, want, got, field)
		}
		for field, want := range map[string]any{"business": "value", "explicitNull": nil} {
			got, present := DocumentField(&doc, "logical-id", field)
			assert.Equal(t, !deleted, present, field)
			if deleted {
				assert.Nil(t, got, field)
			} else {
				assert.Equal(t, want, got, field)
			}
		}
		got, present := DocumentField(&doc, "logical-id", "unknown")
		assert.False(t, present)
		assert.Nil(t, got)
	}
}
