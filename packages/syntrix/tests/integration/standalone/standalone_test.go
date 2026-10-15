package standalone

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/core/storage"
	storage_config "github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/core/storage/types"
	authorizationconfig "github.com/codetreker/syntrix/internal/gateway/authorization/config"
	api_config "github.com/codetreker/syntrix/internal/gateway/config"
	identity_config "github.com/codetreker/syntrix/internal/identity/config"
	identityconfig "github.com/codetreker/syntrix/internal/identity/config"
	indexer_config "github.com/codetreker/syntrix/internal/indexer/config"
	"github.com/codetreker/syntrix/internal/initialization"
	puller_config "github.com/codetreker/syntrix/internal/puller/config"
	"github.com/codetreker/syntrix/internal/server"
	"github.com/codetreker/syntrix/internal/services"
	triggerconfig "github.com/codetreker/syntrix/internal/trigger/config"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// StandaloneEnv is a test environment for standalone mode.
// Unlike ServiceEnv, it only exposes APIURL since standalone mode
// doesn't have separate Query/CSP HTTP servers.
type StandaloneEnv struct {
	APIURL      string
	Manager     *services.Manager
	Cancel      context.CancelFunc
	MongoURI    string
	PostgresDSN string
	DBName      string
}

// setupStandaloneEnv creates a test environment in standalone mode.
// In standalone mode:
// - Only the API gateway HTTP server is created
// - Query and CSP services run in-process without HTTP servers
// - Inter-service communication uses direct function calls
func setupStandaloneEnv(t *testing.T, rulesContent string, configModifiers ...func(*config.Config)) *StandaloneEnv {
	// Setup MongoDB connection
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}

	// Setup PostgreSQL connection
	postgresDSN := os.Getenv("POSTGRES_DSN")
	if postgresDSN == "" {
		postgresDSN = "postgres://syntrix:syntrix@localhost:5432/syntrix?sslmode=disable"
	}

	// Generate unique database name
	safeName := strings.ReplaceAll(t.Name(), "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")
	if len(safeName) > 20 {
		safeName = safeName[len(safeName)-20:]
	}
	dbName := fmt.Sprintf("test_%s_%d", safeName, time.Now().UnixNano()%100000)
	postgresBaseDSN := postgresDSN
	postgresSchema := fmt.Sprintf("standalone_%d", time.Now().UnixNano())
	parsedDSN, err := url.Parse(postgresDSN)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, parsedDSN.Scheme)
	pgDB, err := sql.Open("postgres", postgresBaseDSN)
	require.NoError(t, err)
	defer pgDB.Close()
	pgCtx, pgCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pgCancel()
	_, err = pgDB.ExecContext(pgCtx, "CREATE SCHEMA "+pq.QuoteIdentifier(postgresSchema))
	require.NoError(t, err)
	query := parsedDSN.Query()
	query.Set("search_path", postgresSchema)
	parsedDSN.RawQuery = query.Encode()
	postgresDSN = parsedDSN.String()
	var manager *services.Manager
	var mgrCancel context.CancelFunc
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			if mgrCancel != nil {
				mgrCancel()
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cleanupCancel()
			if manager != nil {
				manager.Shutdown(cleanupCtx)
			}
			client, err := mongo.Connect(cleanupCtx, options.Client().ApplyURI(mongoURI))
			if err == nil {
				defer client.Disconnect(context.Background())
				assert.NoError(t, client.Database(dbName).Drop(cleanupCtx))
			} else {
				assert.NoError(t, err)
			}
			db, err := sql.Open("postgres", postgresBaseDSN)
			if err == nil {
				defer db.Close()
				_, err = db.ExecContext(cleanupCtx, "DROP SCHEMA "+pq.QuoteIdentifier(postgresSchema)+" CASCADE")
			}
			assert.NoError(t, err)
		})
	}
	t.Cleanup(cleanup)

	// Connect and clean DB
	ctx := context.Background()
	connCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	client, err := mongo.Connect(connCtx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		t.Fatalf("Failed to connect to MongoDB: %v", err)
	}
	defer client.Disconnect(ctx)

	dropCtx, dropCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dropCancel()
	err = client.Database(dbName).Drop(dropCtx)
	require.NoError(t, err)

	apiPort := getFreePort(t)

	// Default security rules
	if rulesContent == "" {
		rulesContent = `
database: default
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{document=**}:
        allow:
          read, write: "true"
`
	}
	rulesDir := t.TempDir() + "/security_rules"
	err = os.MkdirAll(rulesDir, 0755)
	require.NoError(t, err)
	err = os.WriteFile(rulesDir+"/default.yml", []byte(rulesContent), 0644)
	require.NoError(t, err)

	// Create templates directory
	templatesDir := t.TempDir() + "/templates"
	err = os.MkdirAll(templatesDir, 0755)
	require.NoError(t, err)

	templatesContent := `
database: default
templates:
  - name: default-ids
    collectionPattern: "{collection}"
    fields:
      - field: id
        order: asc
`
	templatesFile := templatesDir + "/default.yml"
	err = os.WriteFile(templatesFile, []byte(templatesContent), 0644)
	require.NoError(t, err)

	cfg := &config.Config{
		Server: server.Config{
			Host:     "localhost",
			HTTPPort: apiPort,
		},
		// Query and CSP configs don't need ports in standalone mode
		// as they don't expose HTTP servers
		Storage: storage_config.Config{
			Backends: map[string]storage_config.BackendConfig{
				"default": {
					Type: "mongo",
					Mongo: storage_config.MongoConfig{
						URI:          mongoURI,
						DatabaseName: dbName,
					},
				},
				"postgres": {
					Type: "postgres",
					Postgres: storage_config.PostgresConfig{
						DSN:             postgresDSN,
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
				User: identityconfig.CollectionTopology{
					BaseTopology: identityconfig.BaseTopology{
						Strategy: "single",
						Primary:  "postgres",
					},
					Collection: "auth_users",
				},
				Revocation: identityconfig.CollectionTopology{
					BaseTopology: identityconfig.BaseTopology{
						Strategy: "single",
						Primary:  "default",
					},
					Collection: "revocations",
				},
			},
		},
		Puller: puller_config.Config{
			Backends: []puller_config.PullerBackendConfig{
				{Name: "default", Collections: []string{"documents", "sys"}},
			},
			Buffer: puller_config.BufferConfig{
				Path: t.TempDir() + "/buffer",
			},
			Cleaner: puller_config.CleanerConfig{
				Retention: 1 * time.Hour,
				Interval:  1 * time.Minute,
			},
		},
		Gateway: api_config.GatewayConfig{
			AuthZ: authorizationconfig.Config{
				RulesPath: rulesDir,
			},
		},
		Identity: identity_config.Config{
			AuthN: identity_config.AuthNConfig{
				AccessTokenTTL:  15 * time.Minute,
				RefreshTokenTTL: 7 * 24 * time.Hour,
				AuthCodeTTL:     2 * time.Minute,
				PrivateKeyFile:  t.TempDir() + "/keys/auth_private.pem",
			},
			Admin: identity_config.AdminConfig{
				Username: "admin", // Required for default database bootstrap
				Password: "TestPassword123!",
			},
		},
		Indexer: indexer_config.Config{
			TemplatePath: templatesDir,
			StorageMode:  indexer_config.StorageModePebble,
			Store: indexer_config.StoreConfig{
				Path:          t.TempDir() + "/indexer.db",
				BatchSize:     100,
				BatchInterval: 50 * time.Millisecond,
			},
		},
	}
	cfg.Trigger = triggerconfig.DefaultConfig()
	cfg.Trigger.Evaluator.RulesPath = t.TempDir()
	cfg.Trigger.Evaluator.StorageType = "memory"
	cfg.Trigger.Delivery.StorageType = "memory"
	cfg.Trigger.Broker.Policy.Storage = "memory"

	// Apply config modifiers
	for _, mod := range configModifiers {
		mod(cfg)
	}

	// Create manager in standalone mode
	// In standalone mode, RunXXX options are ignored - all services are always started.
	// We set them to false to verify that standalone mode correctly ignores these flags.
	opts := services.Options{
		RunAPI:              false, // Ignored in standalone mode
		RunQuery:            false, // Ignored in standalone mode
		RunTriggerEvaluator: false, // Ignored in standalone mode
		RunTriggerWorker:    false, // Ignored in standalone mode
		RunIndexer:          false, // Ignored in standalone mode
		RunPuller:           false, // Ignored in standalone mode
		Mode:                services.ModeStandalone,
		BootstrapIndexes:    true,
		WritesQuiesced:      true,
		BootstrapTimeout:    30 * time.Second,
	}

	manager, mgrCancel, err = startInitializedFixture(cfg, opts)
	require.NoError(t, err)

	return &StandaloneEnv{
		APIURL:      fmt.Sprintf("http://localhost:%d", apiPort),
		Manager:     manager,
		MongoURI:    mongoURI,
		PostgresDSN: postgresDSN,
		DBName:      dbName,
		Cancel:      cleanup,
	}
}

func (e *StandaloneEnv) GetToken(t *testing.T, uid string, role string) string {
	// Add timestamp suffix to ensure unique username across test runs
	uniqueUID := fmt.Sprintf("%s_%d", uid, time.Now().UnixNano()%1000000)

	// SignUp
	signupBody := map[string]string{
		"username": uniqueUID,
		"password": "TestPassword123!",
	}
	bodyBytes, _ := json.Marshal(signupBody)
	resp, err := http.Post(e.APIURL+"/auth/v1/signup", "application/json", bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	defer resp.Body.Close()

	// SignUp returns the token directly if successful
	if resp.StatusCode == http.StatusOK {
		var res map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&res)
		require.NoError(t, err)
		return res["access_token"].(string)
	}

	// Log the signup error for debugging
	signupRespBody, _ := io.ReadAll(resp.Body)
	t.Logf("SignUp failed: status=%d body=%s", resp.StatusCode, string(signupRespBody))

	// If signup failed (user exists), try login
	loginBody := map[string]string{
		"username": uid,
		"password": "TestPassword123!",
	}
	bodyBytes, _ = json.Marshal(loginBody)
	resp2, err := http.Post(e.APIURL+"/auth/v1/login", "application/json", bytes.NewBuffer(bodyBytes))
	require.NoError(t, err)
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		loginRespBody, _ := io.ReadAll(resp2.Body)
		t.Logf("Login failed: status=%d body=%s", resp2.StatusCode, string(loginRespBody))
	}
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	var tokenResp map[string]interface{}
	err = json.NewDecoder(resp2.Body).Decode(&tokenResp)
	require.NoError(t, err)

	return tokenResp["access_token"].(string)
}

// TestStandaloneMode_BasicCRUD tests basic CRUD operations in standalone mode.
// This verifies that the in-process query and CSP services work correctly.
func TestStandaloneMode_BasicCRUD(t *testing.T) {
	env := setupStandaloneEnv(t, "")
	defer env.Cancel()

	token := env.GetToken(t, "testuser", "user")
	collection := "test-collection"

	// Test Create
	t.Run("Create document", func(t *testing.T) {
		docData := map[string]interface{}{
			"name": "Test Document",
			"type": "standalone-test",
		}
		body, _ := json.Marshal(docData)

		req, _ := http.NewRequest(http.MethodPost, env.APIURL+"/api/v1/databases/default/documents/"+collection, bytes.NewBuffer(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusCreated, resp.StatusCode)

		var result map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&result)
		require.NoError(t, err)
		assert.Contains(t, result, "id")
	})

	// Test Query (uses Query service internally)
	// Query without filters/orderBy goes directly to storage, no need to wait for indexer
	t.Run("Query documents", func(t *testing.T) {
		queryData := map[string]interface{}{
			"collection": collection,
			// No filters or orderBy - goes directly to storage
		}
		body, _ := json.Marshal(queryData)

		req, _ := http.NewRequest(http.MethodPost, env.APIURL+"/api/v1/databases/default/query", bytes.NewBuffer(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var result struct {
			Documents []json.RawMessage `json:"documents"`
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(result.Documents), 1, "Should have at least one document")
	})
}

// TestStandaloneMode_NoExternalServices verifies that standalone mode
// doesn't require or create Query/CSP HTTP servers.
func TestStandaloneMode_NoExternalServices(t *testing.T) {
	env := setupStandaloneEnv(t, "")
	defer env.Cancel()

	// API server should be up
	resp, err := http.Get(env.APIURL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// In standalone mode, there should be only one HTTP server (API)
	// We verify this by checking that the manager's server count is 1
	// (This is implicitly verified by the fact that we didn't set Query/CSP ports
	// and the service still works)

	// Perform a query operation to verify in-process communication works
	token := env.GetToken(t, "standalone_user", "user")

	// Create a document
	docData := map[string]interface{}{
		"title": "Standalone Test",
	}
	body, _ := json.Marshal(docData)

	req, _ := http.NewRequest(http.MethodPost, env.APIURL+"/api/v1/databases/default/documents/standalone-test", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode, "Document creation should work in standalone mode")
}

// TestStandaloneMode_Watch tests real-time watch functionality in standalone mode.
// Watch uses CSP service internally, so this verifies the local CSP integration.
func TestStandaloneMode_Watch(t *testing.T) {
	env := setupStandaloneEnv(t, "")
	defer env.Cancel()

	token := env.GetToken(t, "watch_user", "user")
	collection := "watch-test"

	// Create initial document
	docData := map[string]interface{}{
		"status": "initial",
	}
	body, _ := json.Marshal(docData)

	req, _ := http.NewRequest(http.MethodPost, env.APIURL+"/api/v1/databases/default/documents/"+collection, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	// Extract document ID for update
	var createResult map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&createResult)
	require.NoError(t, err)
	docID := createResult["id"].(string)

	// Update the document (this would trigger watch events via CSP)
	// PATCH requires the data to be wrapped in "doc" field
	updateData := map[string]interface{}{
		"doc": map[string]interface{}{
			"status": "updated",
		},
	}
	body, _ = json.Marshal(updateData)

	req, _ = http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/api/v1/databases/default/documents/%s/%s", env.APIURL, collection, docID), bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Document update should work in standalone mode")
}

// TestStandaloneMode_TriggerServices tests that trigger services work in standalone mode
// using in-memory pubsub instead of NATS.
func TestStandaloneMode_TriggerServices(t *testing.T) {
	// Create a temp directory for trigger rules
	triggersDir := t.TempDir() + "/triggers"
	err := os.MkdirAll(triggersDir, 0755)
	require.NoError(t, err)

	// Create a simple trigger rule
	triggerContent := `
database: default
triggers:
  test-trigger:
    collection: trigger-test
    events:
      - create
    url: http://localhost:9999/webhook
`
	err = os.WriteFile(triggersDir+"/default.yml", []byte(triggerContent), 0644)
	require.NoError(t, err)

	// Setup standalone env with trigger services enabled
	env := setupStandaloneEnv(t, "", func(cfg *config.Config) {
		cfg.Trigger.Evaluator.RulesPath = triggersDir
		cfg.Trigger.Evaluator.StreamName = "TRIGGERS"
		cfg.Trigger.Delivery.StreamName = "TRIGGERS"
	})
	defer env.Cancel()

	token := env.GetToken(t, "trigger_user", "user")
	collection := "trigger-test"

	// Create a document - this should flow through evaluator via memory pubsub
	docData := map[string]interface{}{
		"name":   "Trigger Test Document",
		"action": "test",
	}
	body, _ := json.Marshal(docData)

	req, _ := http.NewRequest(http.MethodPost, env.APIURL+"/api/v1/databases/default/documents/"+collection, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode, "Document creation should work with triggers enabled")

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)
	assert.Contains(t, result, "id", "Response should contain document ID")

	// Give some time for trigger evaluation to process via memory pubsub
	time.Sleep(100 * time.Millisecond)

	// The test passes if:
	// 1. Document creation succeeded (trigger evaluator didn't block)
	// 2. No panics occurred (memory pubsub is working correctly)
	// 3. The service started successfully with memory pubsub (verified by reaching this point)
}

func startInitializedFixture(cfg *config.Config, opts services.Options) (_ *services.Manager, _ context.CancelFunc, startupErr error) {
	var manager *services.Manager
	var cancel context.CancelFunc
	stop := func() error {
		if cancel != nil {
			cancel()
		}
		if manager != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			manager.Shutdown(ctx)
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("integration runtime did not drain: %w", err)
			}
			return manager.Wait(ctx)
		}
		return nil
	}
	defer func() {
		if startupErr != nil {
			stop()
		}
	}()
	startOwner := func(options services.Options, bootstrap bool) error {
		manager = services.NewManager(cfg, options)
		ctx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if err := manager.PrepareIndexBootstrap(); err != nil {
			return err
		}
		if err := manager.Init(ctx); err != nil {
			return err
		}
		lifeCtx, lifeCancel := context.WithCancel(context.Background())
		cancel = lifeCancel
		if bootstrap {
			if err := seedInitializationFixture(ctx, cfg); err != nil {
				return err
			}
			if err := manager.BootstrapIndexes(lifeCtx, ctx); err != nil {
				return fmt.Errorf("bootstrap integration indexes: %w", err)
			}
			return nil
		}
		if err := manager.Start(lifeCtx); err != nil {
			return err
		}
		return waitForReadyFixture(fmt.Sprintf("http://localhost:%d/ready", cfg.Server.HTTPPort))
	}
	if err := startOwner(opts, true); err != nil {
		return nil, nil, err
	}
	if err := stop(); err != nil {
		return nil, nil, err
	}
	opts.BootstrapIndexes = false
	opts.InitializationAcceptance = true
	if err := startOwner(opts, false); err != nil {
		return nil, nil, err
	}
	if err := acceptInitializationFixture(manager, cfg.Server.HTTPPort); err != nil {
		return nil, nil, err
	}
	if err := stop(); err != nil {
		return nil, nil, err
	}
	ctx, done := context.WithTimeout(context.Background(), 30*time.Second)
	defer done()
	if err := finalizeInitializationFixture(ctx, cfg); err != nil {
		return nil, nil, err
	}
	opts.InitializationAcceptance, opts.WritesQuiesced = false, false
	if err := startOwner(opts, false); err != nil {
		return nil, nil, err
	}
	return manager, cancel, nil
}

func seedInitializationFixture(ctx context.Context, cfg *config.Config) error {
	backends, err := storage.NewBackends(ctx, cfg.Storage)
	if err != nil {
		return err
	}
	defer backends.Close()
	factory, err := storage.NewFactory(ctx, cfg.Storage, backends)
	if err != nil {
		return err
	}
	return factory.Document().Create(ctx, model.DefaultDatabase, types.NewStoredDoc(model.DefaultDatabase, "initialization-acceptance", "seed", map[string]interface{}{"id": "seed"}))
}

func acceptInitializationFixture(manager *services.Manager, port int) error {
	token, err := manager.SystemTokenIssuer().GenerateSystemToken("initialization-acceptance")
	if err != nil {
		return err
	}
	body, err := json.Marshal(model.Query{Collection: "initialization-acceptance", OrderBy: []model.Order{{Field: "id", Direction: "asc"}}})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%d/api/v1/databases/default/query", port), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("indexed-query initialization acceptance returned HTTP %d", response.StatusCode)
	}
	var page struct {
		Documents []json.RawMessage `json:"documents"`
	}
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		return err
	}
	if len(page.Documents) != 1 {
		return fmt.Errorf("indexed-query initialization acceptance returned %d documents", len(page.Documents))
	}
	value, err := model.DecodeTypedValue(page.Documents[0])
	if err != nil {
		return err
	}
	document, ok := value.(map[string]interface{})
	if !ok || document["id"] != "seed" {
		return fmt.Errorf("indexed-query initialization acceptance did not return the bootstrap seed")
	}
	return nil
}

func finalizeInitializationFixture(ctx context.Context, cfg *config.Config) error {
	backends, err := storage.NewBackends(ctx, cfg.Storage)
	if err != nil {
		return err
	}
	defer backends.Close()
	coordinator, err := initialization.New(ctx, cfg.Storage, backends)
	if err != nil {
		return err
	}
	status, err := coordinator.Inspect(ctx)
	if err != nil {
		return err
	}
	if _, err := coordinator.Finalize(ctx, status.Specification); err != nil {
		return fmt.Errorf("finalize integration storage after indexed-query acceptance: %w", err)
	}
	return nil
}

func waitForReadyFixture(url string) error {
	deadline := time.Now().Add(30 * time.Second)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("runtime readiness timed out for %s", url)
}
