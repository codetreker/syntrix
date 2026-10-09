package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminAPIIntegration(t *testing.T) {
	t.Parallel()
	env := setupServiceEnv(t, "")
	defer env.Cancel()

	adminToken := env.GetToken(t, "syntrix", "admin")
	userToken := env.GetToken(t, "regular-user", "user")

	var regularUserID string

	t.Run("ListUsers_Admin", func(t *testing.T) {
		resp := env.MakeRequest(t, "GET", "/admin/users", nil, adminToken)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var users []map[string]interface{}
		err := json.NewDecoder(resp.Body).Decode(&users)
		require.NoError(t, err)
		resp.Body.Close()

		assert.GreaterOrEqual(t, len(users), 2) // admin-user and regular-user

		// Look for regular-user - username has format {testPrefix}_{uid}_{timestamp}
		for _, u := range users {
			username := u["username"].(string)
			if strings.Contains(username, "_regular-user_") || strings.Contains(username, "regular-user") {
				regularUserID = u["id"].(string)
				break
			}
		}
		require.NotEmpty(t, regularUserID, "regular-user not found")
	})

	t.Run("ListUsers_User_Forbidden", func(t *testing.T) {
		resp := env.MakeRequest(t, "GET", "/admin/users", nil, userToken)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("UpdateUser_Admin", func(t *testing.T) {
		require.NotEmpty(t, regularUserID)
		// Disable regular-user
		body := map[string]interface{}{
			"roles":    []string{"user"},
			"disabled": true,
		}
		resp := env.MakeRequest(t, "PATCH", "/admin/users/"+regularUserID, body, adminToken)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()

		// Verify disabled
		resp = env.MakeRequest(t, "GET", "/admin/users", nil, adminToken)
		var users []map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&users)
		resp.Body.Close()

		var found bool
		for _, u := range users {
			if u["id"] == regularUserID {
				found = true
				assert.True(t, u["disabled"].(bool))
				break
			}
		}
		assert.True(t, found)
	})

	t.Run("PushRules_Admin", func(t *testing.T) {
		// First, get current rules so we can restore them
		resp := env.MakeRequest(t, "GET", "/admin/rules", nil, adminToken)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		originalRules := new(bytes.Buffer)
		originalRules.ReadFrom(resp.Body)
		resp.Body.Close()

		// Push test rules (include all original patterns plus our test pattern to avoid breaking other tests)
		rules := `
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /public/{doc=**}:
        allow:
          read, write: "true"
      /private/{doc=**}:
        allow:
          read, write: "request.auth.userId != null"
      /admin/{doc=**}:
        allow:
          read, write: "request.auth.roles != null && 'admin' in request.auth.roles"
      /test/{doc=**}:
        allow:
          read: "true"
      /{document=**}:
        allow:
          read, write: "true"
`
		req, err := http.NewRequest("POST", env.APIURL+"/admin/rules/push", bytes.NewBufferString(rules))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+adminToken)

		client := &http.Client{}
		resp, err = client.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()

		// Verify rules updated (by fetching them)
		resp = env.MakeRequest(t, "GET", "/admin/rules", nil, adminToken)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// The response is the rules content
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		resp.Body.Close()

		assert.Contains(t, buf.String(), "/test")

		// Restore original rules (optional - keeps the test from breaking others)
		req, err = http.NewRequest("POST", env.APIURL+"/admin/rules/push", bytes.NewBufferString(originalRules.String()))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		resp, err = client.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
	})
}
