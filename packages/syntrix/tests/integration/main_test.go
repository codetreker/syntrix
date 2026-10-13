package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	deploymentconfig "github.com/codetreker/syntrix/deployment/config"
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
	query_config "github.com/codetreker/syntrix/internal/query/config"
	"github.com/codetreker/syntrix/internal/server"
	"github.com/codetreker/syntrix/internal/services"
	"github.com/codetreker/syntrix/internal/streamer"
	trigger_config "github.com/codetreker/syntrix/internal/trigger/config"
	"github.com/codetreker/syntrix/internal/trigger/delivery"
	"github.com/codetreker/syntrix/internal/trigger/evaluator"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
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
	postgresSchema  string
	postgresBaseDSN string
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
func setupGlobalEnv() (_ *GlobalTestEnv, setupErr error) {
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
	resources := &GlobalTestEnv{MongoURI: mongoURI, DBName: dbName}
	defer func() {
		if setupErr != nil {
			resources.Shutdown()
		}
	}()

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
	postgresBaseDSN := postgresDSN
	postgresSchema := fmt.Sprintf("integration_%d", time.Now().UnixNano())
	if _, err := pgDB.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(postgresSchema)); err != nil {
		pgDB.Close()
		return nil, fmt.Errorf("create isolated PostgreSQL schema: %w", err)
	}
	resources.postgresBaseDSN, resources.postgresSchema = postgresBaseDSN, postgresSchema
	pgDB.Close()
	parsedDSN, err := url.Parse(postgresDSN)
	if err != nil || (parsedDSN.Scheme != "postgres" && parsedDSN.Scheme != "postgresql") {
		return nil, fmt.Errorf("integration PostgreSQL fixture requires a PostgreSQL URL")
	}
	query := parsedDSN.Query()
	query.Set("search_path", postgresSchema)
	parsedDSN.RawQuery = query.Encode()
	postgresDSN = parsedDSN.String()

	// Create temp directories for test files
	tempDir, err := os.MkdirTemp("", "integration_test_")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	resources.tempDir = tempDir

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
	resources.WebhookServer = webhookServer

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
    condition: "has(event.document.age) && event.document.age >= 18"
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
	resources.NatsURL, resources.StreamName = natsURL, streamName
	broker := deploymentconfig.DefaultOperatorBinding()
	broker.StreamName, broker.SubjectPrefix = streamName, streamName
	broker.ConsumerName = streamName + "_DELIVERY"
	broker.Ownership.InstanceID = strings.ReplaceAll(uuid.NewString(), "-", "")
	broker.Ownership.AllocationID = strings.ReplaceAll(uuid.NewString(), "-", "")
	broker.Policy.Storage = "memory"

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
			AuthZ: authorizationconfig.Config{
				RulesPath: rulesDir,
			},
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
		Identity: identity_config.Config{
			AuthN: identity_config.AuthNConfig{
				AccessTokenTTL:  15 * time.Minute,
				RefreshTokenTTL: 7 * 24 * time.Hour,
				AuthCodeTTL:     2 * time.Minute,
				PrivateKeyFile:  keysDir + "/auth_private.pem",
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
			Broker:  &broker,
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
				ConsumerName: broker.ConsumerName,
				StorageType:  "memory", // Use memory for tests
			},
		},
	}
	cfg.Trigger.ApplyDefaults()

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

	manager, mgrCancel, err := startInitializedFixture(cfg, opts)
	if err != nil {
		return nil, err
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
		postgresSchema:  postgresSchema,
		postgresBaseDSN: postgresBaseDSN,
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
	if connection, err := nats.Connect(e.NatsURL, nats.Timeout(2*time.Second)); err == nil {
		defer connection.Close()
		if broker, err := jetstream.New(connection); err == nil {
			if err := broker.DeleteStream(shutdownCtx, e.StreamName); err != nil && err != jetstream.ErrStreamNotFound {
				log.Printf("Delete integration stream: %v", err)
			}
		}
	}
	if e.postgresSchema != "" {
		if db, err := sql.Open("postgres", e.postgresBaseDSN); err == nil {
			defer db.Close()
			if _, err := db.ExecContext(shutdownCtx, "DROP SCHEMA "+pq.QuoteIdentifier(e.postgresSchema)+" CASCADE"); err != nil {
				log.Printf("Drop integration PostgreSQL schema: %v", err)
			}
		}
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
	authService := e.Manager.SystemTokenIssuer()
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
		return waitForHealthWithTimeout(fmt.Sprintf("http://localhost:%d/ready", cfg.Server.HTTPPort), 30*time.Second)
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
	response, err := httpClientWithTimeout.Do(request)
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
