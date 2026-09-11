package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	storage_config "github.com/syntrixbase/syntrix/internal/core/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthzIntegration(t *testing.T) {
	t.Parallel()
	// 1. Define Rules
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
          read, write: "'admin' in request.auth.roles"
`

	// 2. Setup Environment
	env := setupServiceEnv(t, rules)
	defer env.Cancel()

	// 3. Insert Test Data
	// We need to connect to Mongo to insert documents directly
	ctx := context.Background()
	connCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cfg := storage_config.Config{
		Backends: map[string]storage_config.BackendConfig{
			"default": {
				Type: "mongo",
				Mongo: storage_config.MongoConfig{
					URI:          env.MongoURI,
					DatabaseName: env.DBName,
				},
			},
			"postgres": {
				Type: "postgres",
				Postgres: storage_config.PostgresConfig{
					DSN:             GetGlobalEnv().PostgresDSN,
					MaxOpenConns:    10,
					MaxIdleConns:    5,
					ConnMaxLifetime: 5 * time.Minute,
				},
			},
		},
		Topology: storage_config.TopologyConfig{
			Document: storage_config.DocumentTopology{
				BaseTopology: storage_config.BaseTopology{
					Strategy: "single",
					Primary:  "default",
				},
				DataCollection: "documents",
				SysCollection:  "sys",
			},
			User: storage_config.CollectionTopology{
				BaseTopology: storage_config.BaseTopology{
					Strategy: "single",
					Primary:  "postgres",
				},
				Collection: "auth_users",
			},
			Revocation: storage_config.CollectionTopology{
				BaseTopology: storage_config.BaseTopology{
					Strategy: "single",
					Primary:  "default",
				},
				Collection: "revocations",
			},
		},
	}
	factory, err := storage.NewFactory(connCtx, cfg)
	require.NoError(t, err)
	defer factory.Close()
	backend := factory.Document()

	docs := []storage.StoredDoc{
		storage.NewStoredDoc("default", "public", "doc1", map[string]interface{}{"foo": "bar"}),
		storage.NewStoredDoc("default", "private", "doc1", map[string]interface{}{"secret": "data"}),
		storage.NewStoredDoc("default", "admin", "doc1", map[string]interface{}{"top": "secret"}),
	}
	for _, d := range docs {
		err := backend.Create(ctx, "default", d)
		require.NoError(t, err)
	}

	// Helper to make requests
	makeRequest := func(method, path, token string, body interface{}) int {
		var bodyBytes []byte
		if body != nil {
			bodyBytes, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, env.APIURL+path, bytes.NewBuffer(bodyBytes))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// 4. Test Scenarios

	// Scenario 1: Public Access (Token required for database context)
	t.Run("Public Access", func(t *testing.T) {
		token := env.GetToken(t, "user-public", "user")
		require.NotEmpty(t, token)

		code := makeRequest("GET", "/api/v1/databases/default/documents/public/doc1", token, nil)
		assert.Equal(t, http.StatusOK, code)
	})

	// Scenario 2: Private Access (No Token) -> Deny (missing database)
	t.Run("Private Access No Token", func(t *testing.T) {
		code := makeRequest("GET", "/api/v1/databases/default/documents/private/doc1", "", nil)
		assert.Equal(t, http.StatusForbidden, code)
	})

	// Scenario 3: Private Access (With Token) -> Allow
	t.Run("Private Access With Token", func(t *testing.T) {
		token := env.GetToken(t, "user1", "user")
		require.NotEmpty(t, token)

		code := makeRequest("GET", "/api/v1/databases/default/documents/private/doc1", token, nil)
		assert.Equal(t, http.StatusOK, code)
	})

	// Scenario 4: Admin Access (User Token) -> Deny
	t.Run("Admin Access User Token", func(t *testing.T) {
		token := env.GetToken(t, "user2", "user")
		require.NotEmpty(t, token)

		code := makeRequest("GET", "/api/v1/databases/default/documents/admin/doc1", token, nil)
		assert.Equal(t, http.StatusForbidden, code)
	})

	// Scenario 5: Admin Access (Admin Token) -> Allow
	t.Run("Admin Access Admin Token", func(t *testing.T) {
		token := env.GetToken(t, "syntrix", "admin")
		require.NotEmpty(t, token)

		code := makeRequest("GET", "/api/v1/databases/default/documents/admin/doc1", token, nil)
		assert.Equal(t, http.StatusOK, code)
	})
}
