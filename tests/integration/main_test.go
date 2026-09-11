package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/syntrixbase/syntrix/internal/config"
	identity_config "github.com/syntrixbase/syntrix/internal/core/identity/config"
	storage_config "github.com/syntrixbase/syntrix/internal/core/storage/config"
	api_config "github.com/syntrixbase/syntrix/internal/gateway/config"
	indexer_config "github.com/syntrixbase/syntrix/internal/indexer/config"
	puller_config "github.com/syntrixbase/syntrix/internal/puller/config"
	query_config "github.com/syntrixbase/syntrix/internal/query/config"
	"github.com/syntrixbase/syntrix/internal/server"
	"github.com/syntrixbase/syntrix/internal/services"
	"github.com/syntrixbase/syntrix/internal/streamer"
	trigger_config "github.com/syntrixbase/syntrix/internal/trigger/config"
	"github.com/syntrixbase/syntrix/internal/trigger/delivery"
	"github.com/syntrixbase/syntrix/internal/trigger/evaluator"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// HTTP client with timeout for health checks
var httpClientWithTimeout = &http.Client{Timeout: 5 * time.Second}

// Global service instance shared by all integration tests
var (
	globalEnv   *GlobalTestEnv
	globalEnvMu sync.RWMutex
)

// GlobalTestEnv holds the shared test environment
type GlobalTestEnv struct {
	APIURL          string
	QueryURL        string
	Manager         *services.Manager
	MongoURI        string
	PostgresDSN     string
	DBName          string
	TriggerRulesDir string
	StreamName      string
	NatsURL         string
	WebhookServer   *WebhookTestServer
	cancel          context.CancelFunc
	tempDir         string
}

// WebhookTestServer captures webhook deliveries for testing
type WebhookTestServer struct {
	Server   *http.Server
	URL      string
	mu       sync.Mutex
	received []WebhookDelivery
	notify   chan struct{}
}

// WebhookDelivery represents a single webhook delivery
// This structure matches the DeliveryTask sent by the trigger worker
type WebhookDelivery struct {
	TriggerID      string                 `json:"triggerId"`
	Database       string                 `json:"database"`
	Event          string                 `json:"event"`
	Collection     string                 `json:"collection"`
	DocumentID     string                 `json:"documentId"`
	LSN            string                 `json:"lsn"`
	Seq            int64                  `json:"seq"`
	Before         map[string]interface{} `json:"before,omitempty"`
	After          map[string]interface{} `json:"after,omitempty"`
	Timestamp      int64                  `json:"ts"`
	URL            string                 `json:"url"`
	HeadersFromReq map[string]string      `json:"headers"`
	SecretsRef     string                 `json:"secretsRef"`
	RetryPolicy    json.RawMessage        `json:"retryPolicy"`
	Timeout        json.RawMessage        `json:"timeout"`
	PreIssuedToken string                 `json:"preIssuedToken,omitempty"`
	Payload        map[string]interface{} `json:"payload,omitempty"`
	SubjectHashed  bool                   `json:"subjectHashed,omitempty"`
	Headers        http.Header            `json:"-"` // HTTP headers from request
	ReceivedAt     time.Time              `json:"-"`
}

// NewWebhookTestServer creates a new webhook test server
func NewWebhookTestServer() (*WebhookTestServer, error) {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return nil, err
	}

	ws := &WebhookTestServer{
		received: make([]WebhookDelivery, 0),
		notify:   make(chan struct{}, 100),
	}
	ws.URL = fmt.Sprintf("http://localhost:%d/webhook", listener.Addr().(*net.TCPAddr).Port)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", ws.handleWebhook)

	ws.Server = &http.Server{Handler: mux}
	go ws.Server.Serve(listener)

	return ws, nil
}

func (ws *WebhookTestServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	var delivery WebhookDelivery
	if err := json.NewDecoder(r.Body).Decode(&delivery); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	delivery.Headers = r.Header
	delivery.ReceivedAt = time.Now()

	ws.mu.Lock()
	ws.received = append(ws.received, delivery)
	ws.mu.Unlock()

	select {
	case ws.notify <- struct{}{}:
	default:
	}

	w.WriteHeader(http.StatusOK)
}

// WaitForDelivery waits for a webhook delivery matching the trigger ID
func (ws *WebhookTestServer) WaitForDelivery(triggerID string, timeout time.Duration) (*WebhookDelivery, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ws.mu.Lock()
		for _, d := range ws.received {
			if d.TriggerID == triggerID {
				ws.mu.Unlock()
				return &d, nil
			}
		}
		ws.mu.Unlock()

		select {
		case <-ws.notify:
			// Check again
		case <-time.After(100 * time.Millisecond):
			// Timeout, check again
		}
	}
	return nil, fmt.Errorf("timeout waiting for delivery with triggerID %s", triggerID)
}

// WaitForDeliveryMatching waits for a webhook delivery matching the given criteria
func (ws *WebhookTestServer) WaitForDeliveryMatching(triggerID, collection, event string, timeout time.Duration) (*WebhookDelivery, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ws.mu.Lock()
		for _, d := range ws.received {
			if d.TriggerID == triggerID && d.Collection == collection && d.Event == event {
				ws.mu.Unlock()
				return &d, nil
			}
		}
		ws.mu.Unlock()

		select {
		case <-ws.notify:
			// Check again
		case <-time.After(100 * time.Millisecond):
			// Timeout, check again
		}
	}
	return nil, fmt.Errorf("timeout waiting for delivery with triggerID=%s, collection=%s, event=%s", triggerID, collection, event)
}

// GetDeliveries returns all received deliveries
func (ws *WebhookTestServer) GetDeliveries() []WebhookDelivery {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	result := make([]WebhookDelivery, len(ws.received))
	copy(result, ws.received)
	return result
}

// HasDeliveryForCollection checks if any delivery was received for a collection
func (ws *WebhookTestServer) HasDeliveryForCollection(collection string) bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for _, d := range ws.received {
		if d.Collection == collection {
			return true
		}
	}
	return false
}

// Shutdown stops the webhook server
func (ws *WebhookTestServer) Shutdown(ctx context.Context) error {
	return ws.Server.Shutdown(ctx)
}

// TestMain sets up the global test environment before running tests
func TestMain(m *testing.M) {
	// Setup global environment
	env, err := setupGlobalEnv()
	if err != nil {
		log.Fatalf("Failed to setup global test environment: %v", err)
	}
	globalEnv = env

	// Run tests
	code := m.Run()

	// Cleanup
	if globalEnv != nil {
		globalEnv.Shutdown()
	}

	os.Exit(code)
}

// GetGlobalEnv returns the shared global test environment
// Safe for concurrent access
func GetGlobalEnv() *GlobalTestEnv {
	globalEnvMu.RLock()
	defer globalEnvMu.RUnlock()
	return globalEnv
}

// setupGlobalEnv creates and starts the global service instance
func setupGlobalEnv() (*GlobalTestEnv, error) {
	// MongoDB connection
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}

	// PostgreSQL connection
	postgresDSN := os.Getenv("POSTGRES_DSN")
	if postgresDSN == "" {
		postgresDSN = "postgres://syntrix:syntrix@localhost:5432/syntrix?sslmode=disable"
	}

	// Use a single database for all tests, with collection-level isolation
	dbName := fmt.Sprintf("integration_test_%d", time.Now().UnixNano())

	// Connect and ensure MongoDB is clean
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}
	defer client.Disconnect(context.Background())

	if err := client.Database(dbName).Drop(ctx); err != nil {
		return nil, fmt.Errorf("failed to drop database: %w", err)
	}

	// Verify PostgreSQL connection
	pgDB, err := sql.Open("postgres", postgresDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open PostgreSQL connection: %w", err)
	}
	if err := pgDB.PingContext(ctx); err != nil {
		pgDB.Close()
		return nil, fmt.Errorf("failed to ping PostgreSQL: %w", err)
	}
	pgDB.Close()

	// Create temp directories for test files
	tempDir, err := os.MkdirTemp("", "integration_test_")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	keysDir := tempDir + "/keys"
	if err := os.MkdirAll(keysDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create keys dir: %w", err)
	}

	bufferDir := tempDir + "/buffer"
	if err := os.MkdirAll(bufferDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create buffer dir: %w", err)
	}

	// Create comprehensive security rules directory that supports all test scenarios
	// Different tests use different path prefixes to test different authorization rules
	rulesContent := `
database: default
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      # Public paths - no auth required (for authz tests)
      /public/{doc=**}:
        allow:
          read, write: "true"
      # Private paths - require authentication (for authz tests)
      /private/{doc=**}:
        allow:
          read, write: "request.auth.userId != null"
      # Admin paths - require admin role (for authz tests)
      /admin/{doc=**}:
        allow:
          read, write: "request.auth.roles != null && 'admin' in request.auth.roles"
      # Default - allow all (for most tests)
      /{document=**}:
        allow:
          read, write: "true"
`
	rulesDir := tempDir + "/security_rules"
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create rules dir: %w", err)
	}
	// Create rules for all databases used in tests
	databases := []string{"default", "database-a", "database-b", "db1", "db2"}
	for _, db := range databases {
		dbRulesContent := strings.Replace(rulesContent, "database: default", "database: "+db, 1)
		if err := os.WriteFile(rulesDir+"/"+db+".yml", []byte(dbRulesContent), 0644); err != nil {
			return nil, fmt.Errorf("failed to write rules file for %s: %w", db, err)
		}
	}

	// Create templates directory for Indexer
	templatesDir := tempDir + "/templates"
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create templates dir: %w", err)
	}
	templatesContent := `
database: default
templates:
  - name: default-id
    collectionPattern: "{collection}"
    fields:
      - field: id
        order: asc
  - name: default-name
    collectionPattern: "{collection}"
    fields:
      - field: name
        order: asc
  - name: default-title
    collectionPattern: "{collection}"
    fields:
      - field: title
        order: asc
  - name: nested-id
    collectionPattern: "{col1}/{doc1}/{col2}"
    fields:
      - field: id
        order: asc
  - name: nested-name
    collectionPattern: "{col1}/{doc1}/{col2}"
    fields:
      - field: name
        order: asc

  - name: products-composite
    collectionPattern: "products"
    fields:
      - field: category
        order: asc
      - field: price
        order: asc

  # Additional templates for TestAPIQueryAdvanced
  - name: products-category-price-desc
    collectionPattern: "products"
    fields:
      - field: category
        order: asc
      - field: price
        order: desc

  - name: products-stock
    collectionPattern: "products"
    fields:
      - field: stock
        order: asc

  - name: products-id
    collectionPattern: "products"
    fields:
      - field: id
        order: asc

  - name: products-id-deleted
    collectionPattern: "products"
    includeDeleted: true
    fields:
      - field: id
        order: asc
`
	for _, database := range databases {
		content := strings.Replace(templatesContent, "database: default", "database: "+database, 1)
		if err := os.WriteFile(templatesDir+"/"+database+".yml", []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("failed to write templates file: %w", err)
		}
	}

	// Create trigger rules directory for trigger integration tests
	triggerRulesDir := tempDir + "/trigger_rules"
	if err := os.MkdirAll(triggerRulesDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create trigger rules dir: %w", err)
	}

	// Start webhook test server for trigger deliveries
	webhookServer, err := NewWebhookTestServer()
	if err != nil {
		return nil, fmt.Errorf("failed to create webhook test server: %w", err)
	}

	// Create trigger rules file with webhook server URL
	// This trigger watches any collection and fires when age >= 18
	// Using "*" as collection pattern matches any collection name
	triggerRulesContent := fmt.Sprintf(`database: default
triggers:
  integration-test-trigger:
    collection: "*"
    events:
      - create
      - update
    condition: "event.document.age >= 18"
    url: "%s"
    headers:
      X-Test: "true"
`, webhookServer.URL)
	triggerRulesFile := triggerRulesDir + "/default.yml"
	if err := os.WriteFile(triggerRulesFile, []byte(triggerRulesContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write trigger rules file: %w", err)
	}

	// Get NATS URL from environment or use default
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	// Generate unique stream name to avoid conflicts between test runs
	streamName := fmt.Sprintf("TRIGGERS_%d", time.Now().UnixNano())

	// Get available ports
	apiPort, err := getAvailablePort()
	if err != nil {
		return nil, fmt.Errorf("failed to get API port: %w", err)
	}
	grpcPort, err := getAvailablePort()
	if err != nil {
		return nil, fmt.Errorf("failed to get gRPC port: %w", err)
	}

	cfg := &config.Config{
		Server: server.Config{
			Host:     "localhost",
			HTTPPort: apiPort,
			GRPCPort: grpcPort,
		},
		Gateway: api_config.GatewayConfig{
			QueryServiceURL:    fmt.Sprintf("localhost:%d", grpcPort),
			StreamerServiceURL: fmt.Sprintf("localhost:%d", grpcPort),
		},
		Query: query_config.Config{
			IndexerAddr: fmt.Sprintf("localhost:%d", grpcPort),
		},
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
		},
		Identity: identity_config.Config{
			AuthN: identity_config.AuthNConfig{
				AccessTokenTTL:  15 * time.Minute,
				RefreshTokenTTL: 7 * 24 * time.Hour,
				AuthCodeTTL:     2 * time.Minute,
				PrivateKeyFile:  keysDir + "/auth_private.pem",
			},
			AuthZ: identity_config.AuthZConfig{
				RulesPath: rulesDir,
			},
			Admin: identity_config.AdminConfig{
				Username: "admin", // Required for default database bootstrap
				Password: "TestPassword123!",
			},
		},
		Puller: puller_config.Config{
			Backends: []puller_config.PullerBackendConfig{
				{Name: "default", Collections: []string{"documents", "sys"}},
			},
			Cleaner: puller_config.CleanerConfig{
				Interval:  1 * time.Minute,
				Retention: 1 * time.Hour,
			},
			Buffer: puller_config.BufferConfig{
				Path:    bufferDir,
				MaxSize: "100MB",
			},
		},
		Indexer: indexer_config.Config{
			PullerAddr:   fmt.Sprintf("localhost:%d", grpcPort),
			TemplatePath: templatesDir,
			StorageMode:  indexer_config.StorageModePebble,
			Store: indexer_config.StoreConfig{
				Path:          tempDir + "/indexer.db",
				BatchSize:     100,
				BatchInterval: 50 * time.Millisecond,
			},
		},
		Streamer: streamer.Config{
			Server: streamer.ServerConfig{
				PullerAddr: fmt.Sprintf("localhost:%d", grpcPort),
			},
		},
		Trigger: trigger_config.Config{
			NatsURL: natsURL,
			Evaluator: evaluator.Config{
				PullerAddr:         fmt.Sprintf("localhost:%d", grpcPort),
				StartFromNow:       true,
				RulesPath:          triggerRulesDir,
				CheckpointDatabase: "default",
				StreamName:         streamName,
				RetryAttempts:      3,
				StorageType:        "memory", // Use memory for tests
			},
			Delivery: delivery.Config{
				NumWorkers:   4,
				StreamName:   streamName,
				ConsumerName: "trigger-delivery-test",
				StorageType:  "memory", // Use memory for tests
			},
		},
	}

	// Initialize the unified server
	server.InitDefault(cfg.Server, nil)

	opts := services.Options{
		Mode:                services.ModeDistributed, // Services communicate via gRPC even in same process
		RunAPI:              true,
		RunQuery:            true,
		RunStreamer:         true, // Run Streamer service locally
		RunTriggerEvaluator: true, // Enable trigger services for trigger integration tests
		RunTriggerWorker:    true,
		RunPuller:           true,
		RunIndexer:          true,
		BootstrapIndexes:    true,
		WritesQuiesced:      true,
		BootstrapTimeout:    30 * time.Second,
	}

	manager := services.NewManager(cfg, opts)
	if err := manager.Init(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to init manager: %w", err)
	}

	// Start manager
	mgrCtx, mgrCancel := context.WithCancel(context.Background())
	bootstrapCtx, bootstrapCancel := context.WithTimeout(mgrCtx, 30*time.Second)
	err = manager.BootstrapIndexes(mgrCtx, bootstrapCtx)
	bootstrapCancel()
	if err != nil {
		mgrCancel()
		return nil, fmt.Errorf("bootstrap integration indexes: %w", err)
	}
	manager.Start(mgrCtx)

	// Wait for services to be healthy
	if err := waitForHealthWithTimeout(fmt.Sprintf("http://localhost:%d/health", apiPort), 30*time.Second); err != nil {
		mgrCancel()
		return nil, fmt.Errorf("API server failed to start: %w", err)
	}
	// Query service now uses unified gRPC server, no separate health check needed

	log.Printf("[Integration Test] Global environment started (Distributed Mode) - API: %d, gRPC: %d, DB: %s",
		apiPort, grpcPort, dbName)

	return &GlobalTestEnv{
		APIURL:          fmt.Sprintf("http://localhost:%d", apiPort),
		QueryURL:        fmt.Sprintf("localhost:%d", grpcPort),
		Manager:         manager,
		MongoURI:        mongoURI,
		PostgresDSN:     postgresDSN,
		DBName:          dbName,
		TriggerRulesDir: triggerRulesDir,
		StreamName:      streamName,
		NatsURL:         natsURL,
		WebhookServer:   webhookServer,
		cancel:          mgrCancel,
		tempDir:         tempDir,
	}, nil
}

// Shutdown stops the global service and cleans up resources
func (e *GlobalTestEnv) Shutdown() {
	if e.cancel != nil {
		e.cancel()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if e.Manager != nil {
		e.Manager.Shutdown(shutdownCtx)
	}

	// Shutdown webhook server
	if e.WebhookServer != nil {
		e.WebhookServer.Shutdown(shutdownCtx)
	}

	// Cleanup database
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cleanupCancel()

	client, err := mongo.Connect(cleanupCtx, options.Client().ApplyURI(e.MongoURI))
	if err == nil {
		defer client.Disconnect(context.Background())
		_ = client.Database(e.DBName).Drop(cleanupCtx)
	}

	// Cleanup temp files
	if e.tempDir != "" {
		os.RemoveAll(e.tempDir)
	}

	log.Printf("[Integration Test] Global environment shutdown complete")
}

// GenerateSystemToken generates a system token for admin operations
func (e *GlobalTestEnv) GenerateSystemToken() (string, error) {
	authService := e.Manager.AuthService()
	return authService.GenerateSystemToken("system-worker")
}

// getAvailablePort finds an available TCP port
func getAvailablePort() (int, error) {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// waitForHealthWithTimeout waits for a health endpoint to respond
func waitForHealthWithTimeout(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := httpClientWithTimeout.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("health check timed out for %s", url)
}
