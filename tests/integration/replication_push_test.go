package integration

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	storage "github.com/syntrixbase/syntrix/internal/core/storage/types"
	queryclient "github.com/syntrixbase/syntrix/internal/query/client"
	"github.com/syntrixbase/syntrix/pkg/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestReplicationPushPreconditions(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()
	token := env.GetToken(t, "push-preconditions", "user")
	collection := env.testPrefix + "_push"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(env.MongoURI))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		require.NoError(t, client.Disconnect(cleanupCtx))
	})
	documents := client.Database(env.DBName).Collection("documents")
	read := func(id string) (*storage.StoredDoc, error) {
		var doc storage.StoredDoc
		err := documents.FindOne(ctx, bson.M{"database": "default", "fullpath": collection + "/" + id}).Decode(&doc)
		return &doc, err
	}
	change := func(action, id string, version *int64) map[string]any {
		doc := map[string]any{"id": id, "value": action}
		if version != nil {
			doc["version"] = *version
		}
		return map[string]any{"action": action, "document": encodePushDocument(t, doc)}
	}
	push := func(changes ...map[string]any) []map[string]json.RawMessage {
		resp := env.MakeRequest(t, http.MethodPost, "/replication/v1/databases/default/push", map[string]any{
			"collection": collection, "changes": changes,
		}, token)
		defer resp.Body.Close()
		var result struct {
			Conflicts []map[string]json.RawMessage `json:"conflicts"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return result.Conflicts
	}
	assertConflict := func(conflict map[string]json.RawMessage, index int, id, reason string) {
		var gotIndex int
		var gotID, gotReason string
		require.NoError(t, json.Unmarshal(conflict["changeIndex"], &gotIndex))
		require.NoError(t, json.Unmarshal(conflict["id"], &gotID))
		require.NoError(t, json.Unmarshal(conflict["reason"], &gotReason))
		require.Equal(t, index, gotIndex)
		require.Equal(t, id, gotID)
		require.Equal(t, reason, gotReason)
		require.Contains(t, conflict, "current")
	}
	zero, one := int64(0), int64(1)
	conditionalCreate := func(id, condition string, version *int64) map[string]any {
		item := change("create", id, version)
		item["createCondition"] = condition
		return item
	}

	t.Run("absent create retry cannot overwrite or resurrect", func(t *testing.T) {
		request := conditionalCreate("absent-create", "absent", nil)
		require.Empty(t, push(request))
		created, err := read("absent-create")
		require.NoError(t, err)
		require.False(t, created.Deleted)
		require.Equal(t, int64(1), created.Version)
		conflicts := push(request)
		require.Len(t, conflicts, 1)
		assertConflict(conflicts[0], 0, "absent-create", "already_exists")
		current := decodePushCurrent(t, conflicts[0]["current"])
		require.Equal(t, created.Version, current["version"])
		afterRetry, err := read("absent-create")
		require.NoError(t, err)
		require.Equal(t, created, afterRetry)

		require.Empty(t, push(change("delete", "absent-create", &one)))
		deleted, err := read("absent-create")
		require.NoError(t, err)
		require.True(t, deleted.Deleted)
		conflicts = push(request)
		require.Len(t, conflicts, 1)
		assertConflict(conflicts[0], 0, "absent-create", "tombstoned")
		current = decodePushCurrent(t, conflicts[0]["current"])
		require.Equal(t, true, current["deleted"])
		require.Equal(t, deleted.Version, current["version"])
		afterRetry, err = read("absent-create")
		require.NoError(t, err)
		require.Equal(t, deleted, afterRetry)
	})

	t.Run("tombstone create requires retained matching deleted version", func(t *testing.T) {
		require.Empty(t, push(change("create", "conditional-live", nil), change("create", "conditional-deleted", nil)))
		require.Empty(t, push(change("delete", "conditional-deleted", &one)))
		live, err := read("conditional-live")
		require.NoError(t, err)
		deleted, err := read("conditional-deleted")
		require.NoError(t, err)
		conflicts := push(
			conditionalCreate("conditional-missing", "tombstone", &one),
			conditionalCreate("conditional-live", "tombstone", &one),
			conditionalCreate("conditional-deleted", "tombstone", &zero),
		)
		require.Len(t, conflicts, 3)
		assertConflict(conflicts[0], 0, "conditional-missing", "missing")
		require.JSONEq(t, "null", string(conflicts[0]["current"]))
		assertConflict(conflicts[1], 1, "conditional-live", "already_exists")
		require.Equal(t, live.Version, decodePushCurrent(t, conflicts[1]["current"])["version"])
		assertConflict(conflicts[2], 2, "conditional-deleted", "tombstoned")
		require.Equal(t, deleted.Version, decodePushCurrent(t, conflicts[2]["current"])["version"])
		_, err = read("conditional-missing")
		require.ErrorIs(t, err, mongo.ErrNoDocuments)
		unchanged, err := read("conditional-live")
		require.NoError(t, err)
		require.Equal(t, live, unchanged)
		unchanged, err = read("conditional-deleted")
		require.NoError(t, err)
		require.Equal(t, deleted, unchanged)

		request := conditionalCreate("conditional-deleted", "tombstone", &deleted.Version)
		require.Empty(t, push(request))
		recreated, err := read("conditional-deleted")
		require.NoError(t, err)
		require.False(t, recreated.Deleted)
		require.Equal(t, int64(1), recreated.Version)
		require.Equal(t, "create", recreated.Data["value"])
		conflicts = push(request)
		require.Len(t, conflicts, 1)
		assertConflict(conflicts[0], 0, "conditional-deleted", "already_exists")
		unchanged, err = read("conditional-deleted")
		require.NoError(t, err)
		require.Equal(t, recreated, unchanged)
	})

	t.Run("invalid later create condition prevents all writes", func(t *testing.T) {
		negative := int64(-1)
		for _, item := range []struct {
			name      string
			action    string
			condition any
			version   *int64
		}{
			{"null", "create", nil, nil},
			{"empty", "create", "", nil},
			{"unknown", "create", "unknown", nil},
			{"update", "update", "absent", nil},
			{"delete", "delete", "tombstone", &one},
			{"absent-zero", "create", "absent", &zero},
			{"tombstone-omitted", "create", "tombstone", nil},
			{"tombstone-negative", "create", "tombstone", &negative},
		} {
			t.Run(item.name, func(t *testing.T) {
				id := "condition-first-" + item.name
				invalidID := "condition-invalid-" + item.name
				invalid := change(item.action, invalidID, item.version)
				invalid["createCondition"] = item.condition
				resp := env.MakeRequest(t, http.MethodPost, "/replication/v1/databases/default/push", map[string]any{
					"collection": collection,
					"changes":    []map[string]any{conditionalCreate(id, "absent", nil), invalid},
				}, token)
				defer resp.Body.Close()
				require.Equal(t, http.StatusBadRequest, resp.StatusCode)
				_, err := read(id)
				require.ErrorIs(t, err, mongo.ErrNoDocuments)
				_, err = read(invalidID)
				require.ErrorIs(t, err, mongo.ErrNoDocuments)
			})
		}
	})

	t.Run("create preserves omitted zero and one versions", func(t *testing.T) {
		for _, item := range []struct {
			id      string
			version *int64
		}{{"omitted", nil}, {"zero", &zero}, {"one", &one}} {
			require.Empty(t, push(change("create", item.id, item.version)))
			doc, err := read(item.id)
			require.NoError(t, err)
			require.Equal(t, int64(1), doc.Version)
			require.False(t, doc.Deleted)
		}
		conflicts := push(change("update", "zero", &zero))
		require.Len(t, conflicts, 1)
		assertConflict(conflicts[0], 0, "zero", "version_mismatch")
		require.Empty(t, push(change("update", "zero", nil)))
		doc, err := read("zero")
		require.NoError(t, err)
		require.Equal(t, int64(2), doc.Version)
	})

	t.Run("conditional missing changes never create", func(t *testing.T) {
		conflicts := push(change("update", "missing", &one), change("delete", "missing", &one))
		require.Len(t, conflicts, 2)
		for index, conflict := range conflicts {
			assertConflict(conflict, index, "missing", "missing")
			require.JSONEq(t, "null", string(conflict["current"]))
		}
		_, err := read("missing")
		require.ErrorIs(t, err, mongo.ErrNoDocuments)
		require.Empty(t, push(change("delete", "missing", nil)))
		_, err = read("missing")
		require.ErrorIs(t, err, mongo.ErrNoDocuments)
	})

	t.Run("conditional deleted changes preserve tombstone", func(t *testing.T) {
		require.Empty(t, push(change("create", "deleted", nil), change("delete", "deleted", &one)))
		before, err := read("deleted")
		require.NoError(t, err)
		require.True(t, before.Deleted)
		conflicts := push(change("update", "deleted", &one), change("delete", "deleted", &one))
		require.Len(t, conflicts, 2)
		for index, conflict := range conflicts {
			assertConflict(conflict, index, "deleted", "tombstoned")
			current := decodePushCurrent(t, conflict["current"])
			require.Equal(t, "deleted", current["id"])
			require.Equal(t, true, current["deleted"])
			require.Equal(t, before.Version, current["version"])
			require.Equal(t, before.CreatedAt, current["createdAt"])
			require.Equal(t, before.UpdatedAt, current["updatedAt"])
			require.NotContains(t, current, "value")
		}
		require.Empty(t, push(change("delete", "deleted", nil)))
		after, err := read("deleted")
		require.NoError(t, err)
		require.Equal(t, before, after)
	})

	t.Run("repeated ID conflict identifies failed batch item", func(t *testing.T) {
		require.Empty(t, push(change("create", "repeated", nil)))
		conflicts := push(change("update", "repeated", &one), change("delete", "repeated", &one))
		require.Len(t, conflicts, 1)
		assertConflict(conflicts[0], 1, "repeated", "version_mismatch")
		current := decodePushCurrent(t, conflicts[0]["current"])
		require.Equal(t, int64(2), current["version"])
		require.Equal(t, "update", current["value"])
		doc, err := read("repeated")
		require.NoError(t, err)
		require.Equal(t, int64(2), doc.Version)
		require.False(t, doc.Deleted)
	})

	t.Run("conflict preserves nested numbers and maximum version", func(t *testing.T) {
		seed := storage.NewStoredDoc("default", collection, "precise", map[string]any{
			"nested": []any{map[string]any{"integer": int64(math.MaxInt64), "double": float64(1)}},
		})
		seed.Version = math.MaxInt64
		_, err := documents.InsertOne(ctx, seed)
		require.NoError(t, err)
		before, err := read("precise")
		require.NoError(t, err)
		staleVersion := int64(math.MaxInt64 - 1)
		conflicts := push(change("update", "precise", &staleVersion))
		require.Len(t, conflicts, 1)
		assertConflict(conflicts[0], 0, "precise", "version_mismatch")
		current := decodePushCurrent(t, conflicts[0]["current"])
		require.Equal(t, int64(math.MaxInt64), current["version"])
		require.Equal(t, seed.Data["nested"], current["nested"])

		remote, err := queryclient.New(env.QueryURL)
		require.NoError(t, err)
		defer remote.Close()
		requestDoc := storage.NewStoredDoc("default", collection, "precise", map[string]any{"value": "update"})
		response, err := remote.Push(ctx, "default", storage.ReplicationPushRequest{
			Collection: collection,
			Changes: []storage.ReplicationPushChange{{
				Action: storage.PushUpdate, Doc: &requestDoc, BaseVersion: &staleVersion,
			}},
		})
		require.NoError(t, err)
		require.Len(t, response.Conflicts, 1)
		require.Equal(t, int64(math.MaxInt64), response.Conflicts[0].Current.Version)
		require.Equal(t, seed.Data["nested"], response.Conflicts[0].Current.Data["nested"])
		after, err := read("precise")
		require.NoError(t, err)
		require.Equal(t, before, after)
	})

	t.Run("invalid later action prevents earlier write", func(t *testing.T) {
		resp := env.MakeRequest(t, http.MethodPost, "/replication/v1/databases/default/push", map[string]any{
			"collection": collection,
			"changes":    []map[string]any{change("create", "invalid-batch", nil), change("", "invalid-action", nil)},
		}, token)
		defer resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		_, err := read("invalid-batch")
		require.ErrorIs(t, err, mongo.ErrNoDocuments)
	})

	t.Run("raw typed values survive create update conflict and pull", func(t *testing.T) {
		rawDoc := json.RawMessage(`{"type":"object","value":{
			"id":{"type":"string","value":"typed-roundtrip"},
			"version":{"type":"int64","value":"1"},
			"nested":{"type":"array","value":[{"type":"object","value":{
				"maximum":{"type":"int64","value":"9223372036854775807"},
				"minimum":{"type":"int64","value":"-9223372036854775808"},
				"integral":{"type":"float64","value":1},
				"type":{"type":"string","value":"business-property"},
				"value":{"type":"null"}
			}}]}
		}}`)
		require.Empty(t, push(map[string]any{"action": "create", "document": rawDoc}))
		require.Empty(t, push(map[string]any{"action": "update", "document": rawDoc}))
		raw, err := documents.FindOne(ctx, bson.M{"database": "default", "fullpath": collection + "/typed-roundtrip"}).Raw()
		require.NoError(t, err)
		require.Equal(t, int64(2), raw.Lookup("version").Int64())
		nested := raw.Lookup("data", "nested").Array().Index(0).Value().Document()
		require.Equal(t, bsontype.Int64, nested.Lookup("maximum").Type)
		require.Equal(t, int64(math.MaxInt64), nested.Lookup("maximum").Int64())
		require.Equal(t, int64(math.MinInt64), nested.Lookup("minimum").Int64())
		require.Equal(t, bsontype.Double, nested.Lookup("integral").Type)
		require.Equal(t, float64(1), nested.Lookup("integral").Double())

		conflicts := push(map[string]any{"action": "update", "document": rawDoc})
		require.Len(t, conflicts, 1)
		assertConflict(conflicts[0], 0, "typed-roundtrip", "version_mismatch")
		var envelope struct {
			Type  string                     `json:"type"`
			Value map[string]json.RawMessage `json:"value"`
		}
		require.NoError(t, json.Unmarshal(conflicts[0]["current"], &envelope))
		require.Equal(t, "object", envelope.Type)
		require.JSONEq(t, `{"type":"int64","value":"2"}`, string(envelope.Value["version"]))
		require.JSONEq(t, `{"type":"array","value":[{"type":"object","value":{
			"maximum":{"type":"int64","value":"9223372036854775807"},
			"minimum":{"type":"int64","value":"-9223372036854775808"},
			"integral":{"type":"float64","value":1},
			"type":{"type":"string","value":"business-property"},
			"value":{"type":"null"}
		}}]}`, string(envelope.Value["nested"]))
		current := decodePushCurrent(t, conflicts[0]["current"])
		token = replicationAdminToken(t, env, "default", token)
		page := pullReplicationPage(t, env, "default", collection, "", 100, token)
		var pulled model.Document
		for _, doc := range page.Documents {
			if doc.GetID() == "typed-roundtrip" {
				pulled = doc
			}
		}
		require.NotNil(t, pulled)
		require.Equal(t, current, map[string]any(pulled))
	})

	t.Run("invalid later typed item prevents earlier write", func(t *testing.T) {
		for _, item := range []struct {
			name string
			doc  string
		}{
			{"legacy", `{"id":"invalid"}`},
			{"unknown-type", `{"type":"object","value":{"id":{"type":"string","value":"invalid"},"nested":{"type":"decimal","value":"1"}}}`},
			{"float-version", `{"type":"object","value":{"id":{"type":"string","value":"invalid"},"version":{"type":"float64","value":1}}}`},
			{"negative-version", `{"type":"object","value":{"id":{"type":"string","value":"invalid"},"version":{"type":"int64","value":"-1"}}}`},
			{"null-version", `{"type":"object","value":{"id":{"type":"string","value":"invalid"},"version":{"type":"null"}}}`},
		} {
			t.Run(item.name, func(t *testing.T) {
				id := "invalid-" + item.name
				resp := env.MakeRequest(t, http.MethodPost, "/replication/v1/databases/default/push", map[string]any{
					"collection": collection,
					"changes":    []map[string]any{change("create", id, nil), {"action": "create", "document": json.RawMessage(item.doc)}},
				}, token)
				defer resp.Body.Close()
				require.Equal(t, http.StatusBadRequest, resp.StatusCode)
				_, err := read(id)
				require.ErrorIs(t, err, mongo.ErrNoDocuments)
			})
		}
	})
}

func encodePushDocument(t *testing.T, doc map[string]any) json.RawMessage {
	t.Helper()
	encoded, err := model.EncodeTypedValue(doc)
	require.NoError(t, err)
	return encoded
}

func decodePushCurrent(t *testing.T, data json.RawMessage) map[string]any {
	t.Helper()
	value, err := model.DecodeTypedValue(data)
	require.NoError(t, err)
	doc, ok := value.(map[string]any)
	require.True(t, ok)
	return doc
}
