package rest

import (
	"strings"

	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/pkg/model"
)

func flattenDocument(doc *storage.StoredDoc) model.Document {
	if doc == nil {
		return nil
	}
	flat := make(model.Document)

	// Copy data
	for k, v := range doc.Data {
		flat[k] = v
	}

	// Ensure ID is present (extract from Fullpath)
	if _, ok := flat["id"]; !ok {
		if idx := strings.LastIndex(doc.Fullpath, "/"); idx != -1 {
			flat["id"] = doc.Fullpath[idx+1:]
		}
	}

	// Add system fields
	flat["version"] = doc.Version
	flat["updatedAt"] = doc.UpdatedAt
	flat["createdAt"] = doc.CreatedAt
	flat["collection"] = doc.Collection
	if doc.Deleted {
		flat["deleted"] = true
	}
	return flat
}
