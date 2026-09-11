package grpc

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/protobuf/proto"
)

func TestStoredDocToProto(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := storedDocToProto(nil)
		assert.Nil(t, result)
	})

	t.Run("full document", func(t *testing.T) {
		doc := &storage.StoredDoc{
			Id:             "database1:abc123",
			Database:       "database1",
			Fullpath:       "users/user1",
			Collection:     "users",
			CollectionHash: "abc",
			Parent:         "",
			UpdatedAt:      1704067200000,
			CreatedAt:      1704067100000,
			Version:        5,
			Data:           map[string]interface{}{"name": "Alice", "age": float64(30)},
			Deleted:        false,
		}

		result := storedDocToProto(doc)

		assert.Equal(t, "database1:abc123", result.Id)
		assert.Equal(t, "database1", result.Database)
		assert.Equal(t, "users/user1", result.Fullpath)
		assert.Equal(t, "users", result.Collection)
		assert.Equal(t, int64(1704067200000), result.UpdatedAt)
		assert.Equal(t, int64(5), result.Version)
		assert.False(t, result.Deleted)

		// Check data is properly serialized
		var data map[string]interface{}
		err := json.Unmarshal(result.Data, &data)
		assert.NoError(t, err)
		assert.Equal(t, "Alice", data["name"])
		assert.Equal(t, float64(30), data["age"])
	})
}

func TestProtoToStoredDoc(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := protoToStoredDoc(nil)
		assert.Nil(t, result)
	})

	t.Run("full document", func(t *testing.T) {
		data, _ := json.Marshal(map[string]interface{}{"name": "Bob"})
		proto := &pb.Document{
			Id:         "database1:xyz",
			Database:   "database1",
			Fullpath:   "users/user2",
			Collection: "users",
			UpdatedAt:  1704067300000,
			CreatedAt:  1704067200000,
			Version:    3,
			Data:       data,
			Deleted:    true,
		}

		result := protoToStoredDoc(proto)

		assert.Equal(t, "database1:xyz", result.Id)
		assert.Equal(t, "database1", result.Database)
		assert.Equal(t, "users/user2", result.Fullpath)
		assert.Equal(t, "users", result.Collection)
		assert.Equal(t, int64(3), result.Version)
		assert.True(t, result.Deleted)
		assert.Equal(t, "Bob", result.Data["name"])
	})
}

func TestModelDocToProto(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := modelDocToProto(nil)
		assert.Nil(t, result)
	})

	t.Run("full document with float64 types", func(t *testing.T) {
		doc := model.Document{
			"id":         "user1",
			"collection": "users",
			"version":    float64(5),
			"updatedAt":  float64(1704067200000),
			"createdAt":  float64(1704067100000),
			"deleted":    false,
			"name":       "Alice",
			"email":      "alice@example.com",
		}

		result := modelDocToProto(doc)

		assert.Equal(t, "user1", result.Id)
		assert.Equal(t, "users", result.Collection)
		assert.Equal(t, int64(5), result.Version)
		assert.Equal(t, int64(1704067200000), result.UpdatedAt)
		assert.Equal(t, int64(1704067100000), result.CreatedAt)

		// Check user data is properly serialized (excludes system fields)
		var data map[string]interface{}
		err := json.Unmarshal(result.Data, &data)
		assert.NoError(t, err)
		assert.Equal(t, "Alice", data["name"])
		assert.Equal(t, "alice@example.com", data["email"])
		// System fields should not be in data
		assert.Nil(t, data["id"])
		assert.Nil(t, data["collection"])
		assert.Nil(t, data["version"])
	})

	t.Run("document with int64 types", func(t *testing.T) {
		doc := model.Document{
			"id":         "user2",
			"collection": "users",
			"version":    int64(10),
			"updatedAt":  int64(1704067300000),
			"createdAt":  int64(1704067200000),
			"deleted":    true,
			"status":     "active",
		}

		result := modelDocToProto(doc)

		assert.Equal(t, "user2", result.Id)
		assert.Equal(t, int64(10), result.Version)
		assert.Equal(t, int64(1704067300000), result.UpdatedAt)
		assert.Equal(t, int64(1704067200000), result.CreatedAt)
		assert.True(t, result.Deleted)
	})

	t.Run("document with int types", func(t *testing.T) {
		doc := model.Document{
			"id":         "user3",
			"collection": "users",
			"version":    int(15),
			"updatedAt":  int(1704067400000),
			"createdAt":  int(1704067300000),
		}

		result := modelDocToProto(doc)

		assert.Equal(t, "user3", result.Id)
		assert.Equal(t, int64(15), result.Version)
		assert.Equal(t, int64(1704067400000), result.UpdatedAt)
		assert.Equal(t, int64(1704067300000), result.CreatedAt)
	})
}

func TestProtoToModelDoc(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := protoToModelDoc(nil)
		assert.Nil(t, result)
	})

	t.Run("full document", func(t *testing.T) {
		data, _ := json.Marshal(map[string]interface{}{"name": "Bob", "age": 25})
		proto := &pb.Document{
			Id:         "user2",
			Collection: "users",
			Version:    3,
			UpdatedAt:  1704067300000,
			CreatedAt:  1704067200000,
			Data:       data,
		}

		result := protoToModelDoc(proto)

		assert.Equal(t, "user2", result["id"])
		assert.Equal(t, "users", result["collection"])
		assert.Equal(t, int64(3), result["version"])
		assert.Equal(t, "Bob", result["name"])
		assert.Equal(t, float64(25), result["age"])
	})
}

func TestFilterConversions(t *testing.T) {
	t.Run("filterToProto", func(t *testing.T) {
		filter := model.Filter{
			Field: "status",
			Op:    model.OpEq,
			Value: "active",
		}

		result := filterToProto(filter)

		assert.Equal(t, "status", result.Field)
		assert.Equal(t, "==", result.Op)

		var value string
		json.Unmarshal(result.Value, &value)
		assert.Equal(t, "active", value)
	})

	t.Run("protoToFilter", func(t *testing.T) {
		value, _ := json.Marshal("pending")
		proto := &pb.Filter{
			Field: "status",
			Op:    "!=",
			Value: value,
		}

		result := protoToFilter(proto)

		assert.Equal(t, "status", result.Field)
		assert.Equal(t, model.OpNe, result.Op)
		assert.Equal(t, "pending", result.Value)
	})

	t.Run("protoToFilter nil", func(t *testing.T) {
		result := protoToFilter(nil)
		assert.Empty(t, result.Field)
	})

	t.Run("filtersToProto", func(t *testing.T) {
		filters := model.Filters{
			{Field: "a", Op: model.OpEq, Value: 1},
			{Field: "b", Op: model.OpGt, Value: 10},
		}

		result := filtersToProto(filters)

		assert.Len(t, result, 2)
		assert.Equal(t, "a", result[0].Field)
		assert.Equal(t, "b", result[1].Field)
	})

	t.Run("protoToFilters", func(t *testing.T) {
		v1, _ := json.Marshal(1)
		v2, _ := json.Marshal(10)
		protos := []*pb.Filter{
			{Field: "a", Op: "==", Value: v1},
			{Field: "b", Op: ">", Value: v2},
		}

		result := protoToFilters(protos)

		assert.Len(t, result, 2)
		assert.Equal(t, "a", result[0].Field)
		assert.Equal(t, model.OpEq, result[0].Op)
	})
}

func TestPullRequestConversions(t *testing.T) {
	t.Run("pullRequestToProto", func(t *testing.T) {
		req := storage.ReplicationPullRequest{
			Collection: "users",
			Checkpoint: 12345,
			Limit:      100,
		}

		result := pullRequestToProto(req)

		assert.Equal(t, "users", result.Collection)
		assert.Equal(t, int64(12345), result.Checkpoint)
		assert.Equal(t, int32(100), result.Limit)
	})

	t.Run("protoToPullRequest", func(t *testing.T) {
		proto := &pb.PullRequest{
			Database:   "database1",
			Collection: "users",
			Checkpoint: 12345,
			Limit:      100,
		}

		result := protoToPullRequest(proto)

		assert.Equal(t, "users", result.Collection)
		assert.Equal(t, int64(12345), result.Checkpoint)
		assert.Equal(t, 100, result.Limit)
	})

	t.Run("protoToPullRequest nil", func(t *testing.T) {
		result := protoToPullRequest(nil)
		assert.Empty(t, result.Collection)
	})

	t.Run("pullResponseToProto", func(t *testing.T) {
		resp := &storage.ReplicationPullResponse{
			Documents: []*storage.StoredDoc{
				{Id: "doc1", Collection: "users"},
				{Id: "doc2", Collection: "users"},
			},
			Checkpoint: 67890,
		}

		result := pullResponseToProto(resp)

		assert.Len(t, result.Documents, 2)
		assert.Equal(t, int64(67890), result.Checkpoint)
	})

	t.Run("pullResponseToProto nil", func(t *testing.T) {
		result := pullResponseToProto(nil)
		assert.Nil(t, result)
	})
}

func TestPushRequestConversions(t *testing.T) {
	for _, version := range []int64{-1, 0, 5, 9007199254740993, math.MaxInt64} {
		t.Run("base version roundtrip/"+strconv.FormatInt(version, 10), func(t *testing.T) {
			change := storage.ReplicationPushChange{
				Doc: &storage.StoredDoc{Id: "doc1", Collection: "users", Version: 1},
			}
			if version >= 0 {
				change.BaseVersion = &version
			}
			encoded := pushChangeToProto(change)
			assert.Equal(t, version, encoded.BaseVersion)
			assert.Equal(t, int64(1), encoded.Document.Version)

			wire, err := proto.Marshal(encoded)
			require.NoError(t, err)
			var received pb.PushChange
			require.NoError(t, proto.Unmarshal(wire, &received))
			decoded := protoToPushChange(&received)
			assert.Equal(t, change.BaseVersion, decoded.BaseVersion)
			assert.Equal(t, "doc1", decoded.Doc.Id)
			assert.Equal(t, int64(1), decoded.Doc.Version)
		})
	}

	t.Run("protoToPushRequest", func(t *testing.T) {
		data, _ := json.Marshal(map[string]interface{}{"name": "test"})
		baseVersion := int64(5)
		proto := &pb.PushRequest{
			Database:   "database1",
			Collection: "users",
			Changes: []*pb.PushChange{
				{
					Document:    &pb.Document{Id: "doc1", Data: data},
					BaseVersion: baseVersion,
				},
			},
		}

		result := protoToPushRequest(proto)

		assert.Equal(t, "users", result.Collection)
		assert.Len(t, result.Changes, 1)
		assert.Equal(t, "doc1", result.Changes[0].Doc.Id)
		assert.NotNil(t, result.Changes[0].BaseVersion)
		assert.Equal(t, int64(5), *result.Changes[0].BaseVersion)
	})

	t.Run("protoToPushRequest nil", func(t *testing.T) {
		result := protoToPushRequest(nil)
		assert.Empty(t, result.Collection)
	})

	t.Run("protoToPushChange with negative baseVersion", func(t *testing.T) {
		change := &pb.PushChange{
			Document:    &pb.Document{Id: "doc1"},
			BaseVersion: -1,
		}

		result := protoToPushChange(change)

		assert.Nil(t, result.BaseVersion) // -1 means no conflict detection
	})

	t.Run("pushResponseToProto", func(t *testing.T) {
		resp := &storage.ReplicationPushResponse{
			Conflicts: []*storage.StoredDoc{
				{Id: "conflict1"},
			},
		}

		result := pushResponseToProto(resp)

		assert.Len(t, result.Conflicts, 1)
	})

	t.Run("pushResponseToProto nil", func(t *testing.T) {
		result := pushResponseToProto(nil)
		assert.Nil(t, result)
	})
}
