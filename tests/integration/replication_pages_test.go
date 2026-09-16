package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

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
