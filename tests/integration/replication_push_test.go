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
	"go.mongodb.org/mongo-driver/bson"
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
		return map[string]any{"action": action, "document": doc}
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
			var current map[string]any
			require.NoError(t, json.Unmarshal(conflict["current"], &current))
			require.Equal(t, "deleted", current["id"])
			require.Equal(t, true, current["deleted"])
			require.Equal(t, float64(before.Version), current["version"])
			require.Equal(t, float64(before.CreatedAt), current["createdAt"])
			require.Equal(t, float64(before.UpdatedAt), current["updatedAt"])
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
		var current map[string]any
		require.NoError(t, json.Unmarshal(conflicts[0]["current"], &current))
		require.Equal(t, float64(2), current["version"])
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
		var current struct {
			Version json.RawMessage              `json:"version"`
			Nested  []map[string]json.RawMessage `json:"nested"`
		}
		require.NoError(t, json.Unmarshal(conflicts[0]["current"], &current))
		require.Equal(t, "9223372036854775807", string(current.Version))
		require.Len(t, current.Nested, 1)
		require.Equal(t, "9223372036854775807", string(current.Nested[0]["integer"]))
		require.Equal(t, "1", string(current.Nested[0]["double"]))

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
}
