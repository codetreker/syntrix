package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/services"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// TestContext provides an isolated test context using the global service
// Each test gets a unique collection prefix to isolate data
type TestContext struct {
	t            *testing.T
	prefix       string // Unique prefix for collection names
	globalEnv    *GlobalTestEnv
	createdUsers []string // Track created users for cleanup
}

// NewTestContext creates a new isolated test context
// The prefix is derived from the test name to ensure uniqueness
func NewTestContext(t *testing.T) *TestContext {
	env := GetGlobalEnv()
	if env == nil {
		t.Fatal("Global test environment not initialized")
	}

	// Create a unique prefix from test name
	// Replace special characters and keep it short
	prefix := strings.ReplaceAll(t.Name(), "/", "_")
	prefix = strings.ReplaceAll(prefix, " ", "_")
	if len(prefix) > 30 {
		prefix = prefix[:30]
	}
	// Add timestamp suffix for uniqueness in case of test retries
	prefix = fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano()%100000)

	tc := &TestContext{
		t:         t,
		prefix:    prefix,
		globalEnv: env,
	}

	// Register cleanup
	t.Cleanup(func() {
		tc.cleanup()
	})

	return tc
}

// Collection returns the full collection name with test prefix
func (tc *TestContext) Collection(name string) string {
	return fmt.Sprintf("%s_%s", tc.prefix, name)
}

// Slug returns a valid database slug with test prefix
// Slugs must match: ^[a-z][a-z0-9-]{2,62}$
func (tc *TestContext) Slug(name string) string {
	// Use only the timestamp part for uniqueness, keep it short
	ts := time.Now().UnixNano() % 1000000
	// Ensure slug starts with letter and contains only valid chars
	slug := fmt.Sprintf("db%d%s", ts, name)
	// Truncate to max 63 chars
	if len(slug) > 63 {
		slug = slug[:63]
	}
	return strings.ToLower(slug)
}

// APIURL returns the API URL
func (tc *TestContext) APIURL() string {
	return tc.globalEnv.APIURL
}

// QueryURL returns the Query service URL
func (tc *TestContext) QueryURL() string {
	return tc.globalEnv.QueryURL
}

// RealtimeURL returns the Realtime URL (same as API URL)
func (tc *TestContext) RealtimeURL() string {
	return tc.globalEnv.APIURL
}

// Manager returns the service manager
func (tc *TestContext) Manager() *services.Manager {
	return tc.globalEnv.Manager
}

// GetToken creates a user and returns an access token
// The username is prefixed to ensure isolation
func (tc *TestContext) GetToken(uid string, role string) string {
	return tc.GetTokenForDatabase("default", uid, role)
}

// GetTokenForDatabase creates a user in a specific database and returns an access token
func (tc *TestContext) GetTokenForDatabase(_, uid, role string) string {
	// Prefix the username to ensure isolation
	prefixedUID := fmt.Sprintf("%s_%s", tc.prefix, uid)
	tc.createdUsers = append(tc.createdUsers, prefixedUID)

	// Try signup
	signupBody := map[string]string{
		"username": prefixedUID,
		"password": "TestPassword123!",
	}
	bodyBytes, _ := json.Marshal(signupBody)
	resp, err := http.Post(tc.APIURL()+"/auth/v1/signup", "application/json", bytes.NewBuffer(bodyBytes))
	require.NoError(tc.t, err)
	defer resp.Body.Close()

	var token string
	if resp.StatusCode == http.StatusOK {
		var res map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&res)
		require.NoError(tc.t, err)
		token = res["access_token"].(string)
	} else {
		// User might exist, try login
		loginBody := map[string]string{
			"username": prefixedUID,
			"password": "TestPassword123!",
		}
		bodyBytes, _ := json.Marshal(loginBody)
		respLogin, err := http.Post(tc.APIURL()+"/auth/v1/login", "application/json", bytes.NewBuffer(bodyBytes))
		require.NoError(tc.t, err)
		defer respLogin.Body.Close()
		require.Equal(tc.t, http.StatusOK, respLogin.StatusCode)

		var res map[string]interface{}
		err = json.NewDecoder(respLogin.Body).Decode(&res)
		require.NoError(tc.t, err)
		token = res["access_token"].(string)
	}

	// Update role if needed
	if role != "user" {
		tc.updateUserRole(token, role, "", prefixedUID)
		// Re-login to get new token with updated role
		loginBody := map[string]string{
			"username": prefixedUID,
			"password": "TestPassword123!",
		}
		bodyBytes, _ := json.Marshal(loginBody)
		respLogin, err := http.Post(tc.APIURL()+"/auth/v1/login", "application/json", bytes.NewBuffer(bodyBytes))
		require.NoError(tc.t, err)
		defer respLogin.Body.Close()
		require.Equal(tc.t, http.StatusOK, respLogin.StatusCode)

		var res map[string]interface{}
		err = json.NewDecoder(respLogin.Body).Decode(&res)
		require.NoError(tc.t, err)
		token = res["access_token"].(string)
	}

	return token
}

func (tc *TestContext) updateUserRole(token, role, _, _ string) {
	claims, err := parseTokenClaims(token)
	require.NoError(tc.t, err)

	userID := claims["oid"].(string)
	sysToken, err := tc.globalEnv.GenerateSystemToken()
	require.NoError(tc.t, err)

	updateBody := map[string]interface{}{
		"roles": []string{role},
	}
	updateBytes, _ := json.Marshal(updateBody)
	req, err := http.NewRequest("PATCH", tc.APIURL()+"/admin/users/"+userID, bytes.NewBuffer(updateBytes))
	require.NoError(tc.t, err)
	req.Header.Set("Authorization", "Bearer "+sysToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(tc.t, err)
	defer resp.Body.Close()
	require.Equal(tc.t, http.StatusOK, resp.StatusCode)
}

// GenerateSystemToken returns a system token for admin operations
func (tc *TestContext) GenerateSystemToken() string {
	token, err := tc.globalEnv.GenerateSystemToken()
	require.NoError(tc.t, err)
	return token
}

// MakeRequest makes an HTTP request to the API
func (tc *TestContext) MakeRequest(method, path string, body interface{}, token string) *http.Response {
	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		require.NoError(tc.t, err)
		bodyReader = bytes.NewBuffer(jsonBody)
	}

	req, err := http.NewRequest(method, tc.APIURL()+path, bodyReader)
	require.NoError(tc.t, err)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(tc.t, err)
	return resp
}

// CreateDocument creates a document in an isolated collection
func (tc *TestContext) CreateDocument(collection string, data map[string]interface{}, token string) map[string]interface{} {
	resp := tc.MakeRequest("POST", "/api/v1/databases/default/documents/"+tc.Collection(collection), data, token)
	require.Equal(tc.t, http.StatusCreated, resp.StatusCode)
	var doc map[string]interface{}
	err := json.NewDecoder(resp.Body).Decode(&doc)
	require.NoError(tc.t, err)
	resp.Body.Close()
	return doc
}

// GetDocument retrieves a document from an isolated collection
func (tc *TestContext) GetDocument(collection, id, token string) map[string]interface{} {
	resp := tc.MakeRequest("GET", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", tc.Collection(collection), id), nil, token)
	require.Equal(tc.t, http.StatusOK, resp.StatusCode)
	var doc map[string]interface{}
	err := json.NewDecoder(resp.Body).Decode(&doc)
	require.NoError(tc.t, err)
	resp.Body.Close()
	return doc
}

// PatchDocument patches a document in an isolated collection
func (tc *TestContext) PatchDocument(collection, id string, data map[string]interface{}, token string) map[string]interface{} {
	resp := tc.MakeRequest("PATCH", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", tc.Collection(collection), id), data, token)
	require.Equal(tc.t, http.StatusOK, resp.StatusCode)
	var doc map[string]interface{}
	err := json.NewDecoder(resp.Body).Decode(&doc)
	require.NoError(tc.t, err)
	resp.Body.Close()
	return doc
}

// PutDocument replaces a document in an isolated collection
func (tc *TestContext) PutDocument(collection, id string, data map[string]interface{}, token string) map[string]interface{} {
	resp := tc.MakeRequest("PUT", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", tc.Collection(collection), id), data, token)
	require.Equal(tc.t, http.StatusOK, resp.StatusCode)
	var doc map[string]interface{}
	err := json.NewDecoder(resp.Body).Decode(&doc)
	require.NoError(tc.t, err)
	resp.Body.Close()
	return doc
}

// DeleteDocument deletes a document in an isolated collection
func (tc *TestContext) DeleteDocument(collection, id, token string) {
	resp := tc.MakeRequest("DELETE", fmt.Sprintf("/api/v1/databases/default/documents/%s/%s", tc.Collection(collection), id), nil, token)
	require.Equal(tc.t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()
}

// ConnectWebSocket connects to the realtime WebSocket endpoint
func (tc *TestContext) ConnectWebSocket() *websocket.Conn {
	wsURL := "ws" + strings.TrimPrefix(tc.RealtimeURL(), "http") + "/realtime/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(tc.t, err, "Failed to connect to websocket")
	return ws
}

// cleanup removes test data after test completion
func (tc *TestContext) cleanup() {
	// Connect to MongoDB and clean up collections with our prefix
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(tc.globalEnv.MongoURI))
	if err != nil {
		tc.t.Logf("Warning: failed to connect to MongoDB for cleanup: %v", err)
		return
	}
	defer client.Disconnect(context.Background())

	db := client.Database(tc.globalEnv.DBName)

	// List collections and drop those with our prefix
	collections, err := db.ListCollectionNames(ctx, map[string]interface{}{})
	if err != nil {
		tc.t.Logf("Warning: failed to list collections for cleanup: %v", err)
		return
	}

	for _, coll := range collections {
		if strings.HasPrefix(coll, tc.prefix+"_") {
			if err := db.Collection(coll).Drop(ctx); err != nil {
				tc.t.Logf("Warning: failed to drop collection %s: %v", coll, err)
			}
		}
	}
}
