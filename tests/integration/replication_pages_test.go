package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type replicationPage struct {
	Documents  []model.Document
	Checkpoint string
	CaughtUp   bool
}

func replicationAdminToken(t *testing.T, env *ServiceEnv, database, token string) string {
	t.Helper()
	claims, err := parseTokenClaims(token)
	require.NoError(t, err)
	userID, ok := claims["oid"].(string)
	require.True(t, ok)
	username, ok := claims["username"].(string)
	require.True(t, ok)
	resp := env.MakeRequest(t, http.MethodPatch, "/admin/users/"+userID, map[string]any{
		"roles": []string{"user"}, "db_admin": []string{database},
	}, env.GenerateSystemToken(t))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	resp = env.MakeRequest(t, http.MethodPost, "/auth/v1/login", map[string]string{
		"username": username, "password": "TestPassword123!",
	}, "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var login struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&login))
	require.NotEmpty(t, login.AccessToken)
	return login.AccessToken
}

func pullReplicationPage(t *testing.T, env *ServiceEnv, database, collection, checkpoint string, limit int, token string) replicationPage {
	t.Helper()
	resp := env.MakeRequest(t, http.MethodPost, fmt.Sprintf("/replication/v1/databases/%s/pull", database), map[string]any{
		"collection": collection, "checkpoint": checkpoint, "limit": limit,
	}, token)
	defer resp.Body.Close()
	var raw struct {
		Documents  []json.RawMessage `json:"documents"`
		Checkpoint string            `json:"checkpoint"`
		CaughtUp   bool              `json:"caughtUp"`
		Code       string            `json:"code"`
		Message    string            `json:"message"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))
	require.Equal(t, http.StatusOK, resp.StatusCode, "response error: %s %s", raw.Code, raw.Message)
	require.NotEmpty(t, raw.Checkpoint)
	page := replicationPage{Checkpoint: raw.Checkpoint, CaughtUp: raw.CaughtUp}
	for _, encoded := range raw.Documents {
		value, err := model.DecodeTypedValue(encoded)
		require.NoError(t, err)
		document, ok := value.(map[string]any)
		require.True(t, ok, "replication document must be a typed object")
		page.Documents = append(page.Documents, model.Document(document))
	}
	return page
}

func TestReplication_PagesAndCursorValidation(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()
	token := env.GetToken(t, "replication-owner", "user")
	database := "default"
	token = replicationAdminToken(t, env, database, token)
	collection := env.testPrefix + "_pages"
	for _, id := range []string{"alice", "bob", "carol"} {
		resp := env.MakeRequest(t, http.MethodPost, fmt.Sprintf("/replication/v1/databases/%s/push", database), map[string]any{
			"collection": collection,
			"changes":    []map[string]any{{"document": map[string]any{"id": id, "name": id}}},
		}, token)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
	}

	page := pullReplicationPage(t, env, database, collection, "", 1, token)
	require.Len(t, page.Documents, 1)
	assert.False(t, page.CaughtUp)
	require.Equal(t, "alice", page.Documents[0].GetID())
	firstCheckpoint := page.Checkpoint
	resp := env.MakeRequest(t, http.MethodPost, fmt.Sprintf("/replication/v1/databases/%s/push", database), map[string]any{
		"collection": collection,
		"changes": []map[string]any{
			{"document": map[string]any{"id": "aaron", "name": "inserted behind scan cursor"}},
			{"document": map[string]any{"id": "alice", "name": "updated after scan"}},
		},
	}, token)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	seen := map[string]bool{}
	latest := map[string]model.Document{}
	for pageNumber := 0; pageNumber < 30; pageNumber++ {
		require.LessOrEqual(t, len(page.Documents), 1)
		for _, doc := range page.Documents {
			seen[doc.GetID()] = true
			latest[doc.GetID()] = doc
			assert.Equal(t, collection, doc.GetCollection())
			assert.IsType(t, int64(0), doc["version"])
		}
		if page.CaughtUp {
			break
		}
		page = pullReplicationPage(t, env, database, collection, page.Checkpoint, 1, token)
	}
	require.True(t, page.CaughtUp, "bounded scan and replay must reach a source watermark")
	assert.Equal(t, map[string]bool{"aaron": true, "alice": true, "bob": true, "carol": true}, seen)
	assert.Equal(t, "updated after scan", latest["alice"]["name"])
	assert.Equal(t, "inserted behind scan cursor", latest["aaron"]["name"])

	for _, tc := range []struct {
		name       string
		collection string
		checkpoint any
		token      string
		status     int
	}{
		{"legacy numeric checkpoint", collection, 12345, token, http.StatusConflict},
		{"wrong collection", "other_collection", firstCheckpoint, token, http.StatusBadRequest},
		{"ordinary user", collection, nil, env.GetToken(t, "ordinary-reader", "user"), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := env.MakeRequest(t, http.MethodPost, fmt.Sprintf("/replication/v1/databases/%s/pull", database), map[string]any{
				"collection": tc.collection, "checkpoint": tc.checkpoint,
			}, tc.token)
			defer resp.Body.Close()
			assert.Equal(t, tc.status, resp.StatusCode)
			if tc.status == http.StatusConflict {
				var failure struct {
					Code string `json:"code"`
				}
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&failure))
				assert.Equal(t, "RESYNC_REQUIRED", failure.Code)
			}
		})
	}
}
