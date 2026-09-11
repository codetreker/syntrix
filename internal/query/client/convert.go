package client

import (
	"encoding/json"

	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// ============================================================================
// model.Document <-> Proto Document conversions
// ============================================================================

// modelDocToProto converts a model.Document to proto Document.
func modelDocToProto(doc model.Document) *pb.Document {
	if doc == nil {
		return nil
	}

	// Extract system fields
	id, _ := doc["id"].(string)
	collection, _ := doc["collection"].(string)

	var version int64
	switch v := doc["version"].(type) {
	case float64:
		version = int64(v)
	case int64:
		version = v
	case int:
		version = int64(v)
	}

	var updatedAt int64
	switch v := doc["updatedAt"].(type) {
	case float64:
		updatedAt = int64(v)
	case int64:
		updatedAt = v
	case int:
		updatedAt = int64(v)
	}

	var createdAt int64
	switch v := doc["createdAt"].(type) {
	case float64:
		createdAt = int64(v)
	case int64:
		createdAt = v
	case int:
		createdAt = int64(v)
	}

	deleted, _ := doc["deleted"].(bool)

	// Marshal user data (exclude system fields)
	userData := make(map[string]interface{})
	for k, v := range doc {
		switch k {
		case "id", "collection", "version", "updatedAt", "createdAt", "deleted":
			// Skip system fields
		default:
			userData[k] = v
		}
	}

	var data []byte
	if len(userData) > 0 {
		data, _ = json.Marshal(userData)
	}

	return &pb.Document{
		Id:         id,
		Collection: collection,
		Version:    version,
		UpdatedAt:  updatedAt,
		CreatedAt:  createdAt,
		Deleted:    deleted,
		Data:       data,
	}
}

// protoToModelDoc converts a proto Document to model.Document.
func protoToModelDoc(doc *pb.Document) model.Document {
	if doc == nil {
		return nil
	}

	result := make(model.Document)

	// Unmarshal user data
	if len(doc.Data) > 0 {
		var data map[string]interface{}
		if err := json.Unmarshal(doc.Data, &data); err == nil {
			for k, v := range data {
				result[k] = v
			}
		}
	}

	// Add system fields
	if doc.Id != "" {
		result["id"] = doc.Id
	}
	if doc.Collection != "" {
		result["collection"] = doc.Collection
	}
	if doc.Version != 0 {
		result["version"] = doc.Version
	}
	if doc.UpdatedAt != 0 {
		result["updatedAt"] = doc.UpdatedAt
	}
	if doc.CreatedAt != 0 {
		result["createdAt"] = doc.CreatedAt
	}
	if doc.Deleted {
		result["deleted"] = doc.Deleted
	}

	return result
}

// ============================================================================
// StoredDoc <-> Proto Document conversions
// ============================================================================

// protoToStoredDoc converts a proto Document to storage.StoredDoc.
func protoToStoredDoc(doc *pb.Document) *storage.StoredDoc {
	if doc == nil {
		return nil
	}

	var data map[string]interface{}
	if len(doc.Data) > 0 {
		_ = json.Unmarshal(doc.Data, &data)
	}

	return &storage.StoredDoc{
		Id:             doc.Id,
		Database:       doc.Database,
		Fullpath:       doc.Fullpath,
		Collection:     doc.Collection,
		CollectionHash: doc.CollectionHash,
		Parent:         doc.Parent,
		UpdatedAt:      doc.UpdatedAt,
		CreatedAt:      doc.CreatedAt,
		Version:        doc.Version,
		Data:           data,
		Deleted:        doc.Deleted,
	}
}

// storedDocToProto converts a storage.StoredDoc to proto Document.
func storedDocToProto(doc *storage.StoredDoc) *pb.Document {
	if doc == nil {
		return nil
	}

	var data []byte
	if doc.Data != nil {
		data, _ = json.Marshal(doc.Data)
	}

	return &pb.Document{
		Id:             doc.Id,
		Database:       doc.Database,
		Fullpath:       doc.Fullpath,
		Collection:     doc.Collection,
		CollectionHash: doc.CollectionHash,
		Parent:         doc.Parent,
		UpdatedAt:      doc.UpdatedAt,
		CreatedAt:      doc.CreatedAt,
		Version:        doc.Version,
		Data:           data,
		Deleted:        doc.Deleted,
	}
}

// ============================================================================
// Filter conversions
// ============================================================================

// filterToProto converts a model.Filter to proto Filter.
func filterToProto(f model.Filter) *pb.Filter {
	var value []byte
	if f.Value != nil {
		value, _ = json.Marshal(f.Value)
	}

	return &pb.Filter{
		Field: f.Field,
		Op:    string(f.Op),
		Value: value,
	}
}

// filtersToProto converts a slice of model.Filter to proto Filters.
func filtersToProto(filters model.Filters) []*pb.Filter {
	result := make([]*pb.Filter, 0, len(filters))
	for _, f := range filters {
		result = append(result, filterToProto(f))
	}
	return result
}

// ============================================================================
// OrderBy conversions
// ============================================================================

// ============================================================================
// Push change conversion
// ============================================================================

// pushChangeToProto converts storage.ReplicationPushChange to proto.
func pushChangeToProto(c storage.ReplicationPushChange) *pb.PushChange {
	var baseVersion int64 = -1
	if c.BaseVersion != nil {
		baseVersion = *c.BaseVersion
	}
	return &pb.PushChange{
		Document:    storedDocToProto(c.Doc),
		BaseVersion: baseVersion,
	}
}
