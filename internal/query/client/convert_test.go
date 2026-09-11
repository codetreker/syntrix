package client

import (
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestModelDocToProto(t *testing.T) {
	t.Run("nil document", func(t *testing.T) {
		result := modelDocToProto(nil)
		assert.Nil(t, result)
	})

	t.Run("empty document", func(t *testing.T) {
		doc := model.Document{}
		result := modelDocToProto(doc)
		assert.NotNil(t, result)
		assert.Empty(t, result.Id)
		assert.Empty(t, result.Data)
	})

	t.Run("document with system fields", func(t *testing.T) {
		doc := model.Document{
			"id":         "doc1",
			"collection": "users",
			"version":    int64(5),
			"updatedAt":  int64(1234567890),
			"createdAt":  int64(1234567800),
			"deleted":    true,
		}
		result := modelDocToProto(doc)

		assert.Equal(t, "doc1", result.Id)
		assert.Equal(t, "users", result.Collection)
		assert.Equal(t, int64(5), result.Version)
		assert.Equal(t, int64(1234567890), result.UpdatedAt)
		assert.Equal(t, int64(1234567800), result.CreatedAt)
		assert.True(t, result.Deleted)
		assert.Empty(t, result.Data)
	})

	t.Run("document with user data", func(t *testing.T) {
		doc := model.Document{
			"id":   "doc1",
			"name": "Alice",
			"age":  30,
		}
		result := modelDocToProto(doc)

		assert.Equal(t, "doc1", result.Id)
		assert.NotEmpty(t, result.Data)
		assert.Contains(t, string(result.Data), "name")
		assert.Contains(t, string(result.Data), "Alice")
	})

	t.Run("document with float64 version", func(t *testing.T) {
		doc := model.Document{
			"version":   float64(10),
			"updatedAt": float64(1234567890),
			"createdAt": float64(1234567800),
		}
		result := modelDocToProto(doc)

		assert.Equal(t, int64(10), result.Version)
		assert.Equal(t, int64(1234567890), result.UpdatedAt)
		assert.Equal(t, int64(1234567800), result.CreatedAt)
	})

	t.Run("document with int version", func(t *testing.T) {
		doc := model.Document{
			"version":   int(15),
			"updatedAt": int(1234567890),
			"createdAt": int(1234567800),
		}
		result := modelDocToProto(doc)

		assert.Equal(t, int64(15), result.Version)
		assert.Equal(t, int64(1234567890), result.UpdatedAt)
		assert.Equal(t, int64(1234567800), result.CreatedAt)
	})
}

func TestProtoToModelDoc(t *testing.T) {
	t.Run("nil proto", func(t *testing.T) {
		result := protoToModelDoc(nil)
		assert.Nil(t, result)
	})

	t.Run("empty proto", func(t *testing.T) {
		proto := &pb.Document{}
		result := protoToModelDoc(proto)
		assert.NotNil(t, result)
		assert.Empty(t, result)
	})

	t.Run("proto with system fields", func(t *testing.T) {
		proto := &pb.Document{
			Id:         "doc1",
			Collection: "users",
			Version:    5,
			UpdatedAt:  1234567890,
			CreatedAt:  1234567800,
			Deleted:    true,
		}
		result := protoToModelDoc(proto)

		assert.Equal(t, "doc1", result["id"])
		assert.Equal(t, "users", result["collection"])
		assert.Equal(t, int64(5), result["version"])
		assert.Equal(t, int64(1234567890), result["updatedAt"])
		assert.Equal(t, int64(1234567800), result["createdAt"])
		assert.Equal(t, true, result["deleted"])
	})

	t.Run("proto with user data", func(t *testing.T) {
		proto := &pb.Document{
			Id:   "doc1",
			Data: []byte(`{"name":"Alice","age":30}`),
		}
		result := protoToModelDoc(proto)

		assert.Equal(t, "doc1", result["id"])
		assert.Equal(t, "Alice", result["name"])
		assert.Equal(t, float64(30), result["age"])
	})

	t.Run("proto with invalid json data", func(t *testing.T) {
		proto := &pb.Document{
			Id:   "doc1",
			Data: []byte(`{invalid json}`),
		}
		result := protoToModelDoc(proto)

		assert.Equal(t, "doc1", result["id"])
		// Invalid JSON data should be ignored
		_, hasName := result["name"]
		assert.False(t, hasName)
	})
}

func TestProtoToStoredDoc(t *testing.T) {
	t.Run("nil proto", func(t *testing.T) {
		result := protoToStoredDoc(nil)
		assert.Nil(t, result)
	})

	t.Run("full proto", func(t *testing.T) {
		proto := &pb.Document{
			Id:             "doc1",
			Database:       "database1",
			Fullpath:       "users/doc1",
			Collection:     "users",
			CollectionHash: "abc123",
			Parent:         "users",
			UpdatedAt:      1234567890,
			CreatedAt:      1234567800,
			Version:        5,
			Data:           []byte(`{"name":"Alice"}`),
			Deleted:        true,
		}
		result := protoToStoredDoc(proto)

		assert.Equal(t, "doc1", result.Id)
		assert.Equal(t, "database1", result.Database)
		assert.Equal(t, "users/doc1", result.Fullpath)
		assert.Equal(t, "users", result.Collection)
		assert.Equal(t, "abc123", result.CollectionHash)
		assert.Equal(t, "users", result.Parent)
		assert.Equal(t, int64(1234567890), result.UpdatedAt)
		assert.Equal(t, int64(1234567800), result.CreatedAt)
		assert.Equal(t, int64(5), result.Version)
		assert.Equal(t, "Alice", result.Data["name"])
		assert.True(t, result.Deleted)
	})

	t.Run("proto with empty data", func(t *testing.T) {
		proto := &pb.Document{
			Id: "doc1",
		}
		result := protoToStoredDoc(proto)

		assert.Equal(t, "doc1", result.Id)
		assert.Nil(t, result.Data)
	})
}

func TestStoredDocToProto(t *testing.T) {
	t.Run("nil stored doc", func(t *testing.T) {
		result := storedDocToProto(nil)
		assert.Nil(t, result)
	})

	t.Run("full stored doc", func(t *testing.T) {
		doc := &storage.StoredDoc{
			Id:             "doc1",
			Database:       "database1",
			Fullpath:       "users/doc1",
			Collection:     "users",
			CollectionHash: "abc123",
			Parent:         "users",
			UpdatedAt:      1234567890,
			CreatedAt:      1234567800,
			Version:        5,
			Data:           map[string]interface{}{"name": "Alice"},
			Deleted:        true,
		}
		result := storedDocToProto(doc)

		assert.Equal(t, "doc1", result.Id)
		assert.Equal(t, "database1", result.Database)
		assert.Equal(t, "users/doc1", result.Fullpath)
		assert.Equal(t, "users", result.Collection)
		assert.Equal(t, "abc123", result.CollectionHash)
		assert.Equal(t, "users", result.Parent)
		assert.Equal(t, int64(1234567890), result.UpdatedAt)
		assert.Equal(t, int64(1234567800), result.CreatedAt)
		assert.Equal(t, int64(5), result.Version)
		assert.True(t, result.Deleted)
		assert.Contains(t, string(result.Data), "Alice")
	})

	t.Run("stored doc with nil data", func(t *testing.T) {
		doc := &storage.StoredDoc{
			Id: "doc1",
		}
		result := storedDocToProto(doc)

		assert.Equal(t, "doc1", result.Id)
		assert.Nil(t, result.Data)
	})
}

func TestFilterToProto(t *testing.T) {
	t.Run("filter with string value", func(t *testing.T) {
		f := model.Filter{
			Field: "name",
			Op:    "eq",
			Value: "Alice",
		}
		result := filterToProto(f)

		assert.Equal(t, "name", result.Field)
		assert.Equal(t, "eq", result.Op)
		assert.Contains(t, string(result.Value), "Alice")
	})

	t.Run("filter with int value", func(t *testing.T) {
		f := model.Filter{
			Field: "age",
			Op:    "gte",
			Value: 18,
		}
		result := filterToProto(f)

		assert.Equal(t, "age", result.Field)
		assert.Equal(t, "gte", result.Op)
		assert.Contains(t, string(result.Value), "18")
	})

	t.Run("filter with nil value", func(t *testing.T) {
		f := model.Filter{
			Field: "deleted",
			Op:    "eq",
			Value: nil,
		}
		result := filterToProto(f)

		assert.Equal(t, "deleted", result.Field)
		assert.Equal(t, "eq", result.Op)
	})
}

func TestFiltersToProto(t *testing.T) {
	t.Run("empty filters", func(t *testing.T) {
		result := filtersToProto(nil)
		assert.Empty(t, result)
	})

	t.Run("multiple filters", func(t *testing.T) {
		filters := model.Filters{
			{Field: "age", Op: "gte", Value: 18},
			{Field: "active", Op: "eq", Value: true},
		}
		result := filtersToProto(filters)

		assert.Len(t, result, 2)
		assert.Equal(t, "age", result[0].Field)
		assert.Equal(t, "active", result[1].Field)
	})
}

func TestPushChangeToProto(t *testing.T) {
	for _, version := range []int64{-1, 0, 5, 9007199254740993, math.MaxInt64} {
		t.Run(strconv.FormatInt(version, 10), func(t *testing.T) {
			change := storage.ReplicationPushChange{
				Doc: &storage.StoredDoc{
					Id:       "doc1",
					Fullpath: "users/doc1",
					Version:  1,
					Data:     map[string]interface{}{"name": "Alice"},
				},
			}
			if version >= 0 {
				change.BaseVersion = &version
			}
			result := pushChangeToProto(change)

			assert.Equal(t, "doc1", result.Document.Id)
			assert.Equal(t, int64(1), result.Document.Version)
			assert.Equal(t, version, result.BaseVersion)
		})
	}
}
