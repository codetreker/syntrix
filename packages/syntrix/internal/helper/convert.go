package helper

import (
	"strings"

	storetypes "github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/pkg/model"
)

func extractIDFromFullpath(fullpath string) string {
	parts := strings.Split(fullpath, "/")
	if len(parts)%2 != 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func FlattenStorageDocument(doc *storetypes.StoredDoc) model.Document {
	if doc == nil {
		return nil
	}
	out := make(model.Document)
	for k, v := range doc.Data {
		out[k] = v
	}
	// Prefer ID from Data, fallback to extracting from Fullpath
	if id, ok := doc.Data["id"].(string); ok && id != "" {
		out.SetID(id)
	} else {
		out.SetID(extractIDFromFullpath(doc.Fullpath))
	}
	out.SetCollection(doc.Collection)
	out["version"] = doc.Version
	out["updatedAt"] = doc.UpdatedAt
	out["createdAt"] = doc.CreatedAt
	if doc.Deleted {
		out["deleted"] = true
	}
	return out
}
