package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/syntrixbase/syntrix/internal/config"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/core/pubsub"
	pubsubtesting "github.com/syntrixbase/syntrix/internal/core/pubsub/testing"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/gateway"
	"github.com/syntrixbase/syntrix/internal/indexer"
	indexer_config "github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/puller"
	puller_config "github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/server"
	"github.com/syntrixbase/syntrix/internal/trigger"
	"github.com/syntrixbase/syntrix/internal/trigger/delivery"
	"github.com/syntrixbase/syntrix/internal/trigger/evaluator"
	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/mongo"
)

func TestManager_AuthServiceGetter(t *testing.T) {
	t.Parallel()
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{})

	assert.Nil(t, mgr.AuthService())
}

func TestManager_Init_StorageError(t *testing.T) {
	t.Parallel()
	cfg := config.LoadConfig()
	if backend, ok := cfg.Storage.Backends["default_mongo"]; ok {
		backend.Mongo.URI = "mongodb://invalid-host:1"
		cfg.Storage.Backends["default_mongo"] = backend
	}
	opt := Options{RunQuery: true}
	mgr := NewManager(cfg, opt)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := mgr.Init(ctx)
	assert.Error(t, err)
}

func TestManager_Init_TokenServiceError(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Identity.AuthN.PrivateKeyFile = "/nonexistent/dir/key.pem"
	opt := Options{RunAPI: true}
	mgr := NewManager(cfg, opt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := mgr.Init(ctx)
	assert.Error(t, err)
}

func TestManager_Init_AuthzRulesLoadError(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Identity.AuthZ.RulesPath = "__missing_rules_file__"
	opts := Options{RunAPI: true}
	mgr := NewManager(cfg, opts)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := mgr.Init(ctx)
	assert.Error(t, err)
}

func TestManager_InitAuthService_GenerateKey(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Identity.AuthN.PrivateKeyFile = filepath.Join(t.TempDir(), "private.pem")
	mgr := NewManager(cfg, Options{RunTriggerWorker: true})

	// Mock storage factory
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}
	defer func() {
		storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
			return storage.NewFactory(ctx, cfg.Storage)
		}
	}()

	// We need to init storage first because initAuthService depends on it

	err := mgr.initAuthService(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.authService)

	_, statErr := os.Stat(cfg.Identity.AuthN.PrivateKeyFile)
	assert.NoError(t, statErr)
}

func TestManager_InitAPIServer_WithRules(t *testing.T) {
	// Initialize a fresh server instance to avoid route conflicts with other tests
	server.InitDefault(server.DefaultConfig(), nil)

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	rulesDir := filepath.Join(t.TempDir(), "security_rules")
	assert.NoError(t, os.MkdirAll(rulesDir, 0755))
	rulesContent := "database: default\nmatch:\n  /databases/{database}/documents/{doc}:\n    allow:\n      get: \"true\"\n"
	assert.NoError(t, os.WriteFile(filepath.Join(rulesDir, "default.yml"), []byte(rulesContent), 0644))
	cfg.Identity.AuthZ.RulesPath = rulesDir

	mgr := NewManager(cfg, Options{})
	mgr.authService = &stubAuthN{}
	querySvc := &stubQueryService{}

	err := mgr.initAPIServer(querySvc)
	assert.NoError(t, err)
	// API routes are now registered directly to the unified server
}

func TestManager_InitAPIServer_NoRules(t *testing.T) {
	// Initialize a fresh server instance to avoid route conflicts with other tests
	server.InitDefault(server.DefaultConfig(), nil)

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = ""

	mgr := NewManager(cfg, Options{})
	mgr.authService = &stubAuthN{}
	querySvc := &stubQueryService{}

	err := mgr.initAPIServer(querySvc)
	assert.NoError(t, err)
	// API routes are now registered directly to the unified server
}

func TestManager_InitAPIServer_WithRealtime(t *testing.T) {
	// Initialize a fresh server instance to avoid route conflicts with other tests
	server.InitDefault(server.DefaultConfig(), nil)

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = ""
	mgr := NewManager(cfg, Options{})
	mgr.authService = &stubAuthN{}

	err := mgr.initAPIServer(&stubQueryService{})
	assert.NoError(t, err)
	assert.NotNil(t, mgr.rtServer)
	// API routes are now registered directly to the unified server
}

func TestManager_InitTriggerServices_NATSFailure(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Trigger.NatsURL = "nats://127.0.0.1:1"
	mgr := NewManager(cfg, Options{RunTriggerWorker: true, Mode: ModeDistributed})

	err := mgr.initTriggerServices(context.Background())
	assert.Error(t, err)
}

func TestManager_InitStorage_SkipsWhenNoServices(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	sf, err := mgr.getStorageFactory(ctx)
	assert.NoError(t, err)
	assert.Nil(t, sf)
}

func TestManager_Init_RunAuthPath(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Identity.AuthN.PrivateKeyFile = filepath.Join(t.TempDir(), "auth.pem")

	// Create a dummy rules directory
	rulesDir := filepath.Join(t.TempDir(), "security_rules")
	os.MkdirAll(rulesDir, 0755)
	os.WriteFile(filepath.Join(rulesDir, "default.yml"), []byte("database: default\nmatch: {}"), 0644)
	cfg.Identity.AuthZ.RulesPath = rulesDir

	mgr := NewManager(cfg, Options{RunAPI: true})

	err := mgr.Init(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.authService)
}

func TestManager_Init_RunQueryPath(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	// Initialize a fresh server instance
	server.InitDefault(server.DefaultConfig(), nil)

	fakeDocStore := &fakeDocumentStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.GRPCPort = 0
	mgr := NewManager(cfg, Options{RunQuery: true})

	err := mgr.Init(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.storageFactory)
	assert.Equal(t, fakeDocStore, mgr.storageFactory.Document())
	// Query service now uses unified gRPC server, not separate HTTP server
	// The service is registered on server.Default()
}

func TestManager_Init_RunRealtimePath(t *testing.T) {
	// Initialize a fresh server instance to avoid route conflicts with other tests
	server.InitDefault(server.DefaultConfig(), nil)

	// Mock storage factory to avoid real database connection
	fakeSF := &stubStorageFactory{
		dbByName: map[string]string{"primary": "db_primary"},
		client:   &mongo.Client{},
	}
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return fakeSF, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0

	cfg.Identity.AuthZ.RulesPath = ""
	mgr := NewManager(cfg, Options{RunAPI: true})

	err := mgr.Init(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.rtServer)
	// API routes are now registered directly to the unified server
}

func TestManager_initPullerService_Success(t *testing.T) {
	fakeSF := &stubStorageFactory{
		dbByName: map[string]string{"primary": "db_primary"},
		client:   &mongo.Client{},
	}

	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return fakeSF, nil
	}

	cfg := config.LoadConfig()
	cfg.Puller.Buffer.Path = t.TempDir()
	cfg.Puller.Backends = []puller_config.PullerBackendConfig{{Name: "primary"}}

	mgr := NewManager(cfg, Options{Mode: ModeDistributed, RunPuller: true})
	defer mgr.Shutdown(context.Background())

	err := mgr.initPullerService(context.Background())
	assert.NoError(t, err)
	names := mgr.pullerService.BackendNames()
	assert.Len(t, names, 1)
	assert.Equal(t, "primary", names[0])
	// Note: pullerGRPC is nil here because initPullerService no longer registers gRPC.
	// gRPC registration is done separately by initPullerGRPCServer() in initDistributed().
	assert.Nil(t, mgr.pullerGRPC)
}

func TestManager_initPullerService_GetMongoError(t *testing.T) {
	fakeSF := &stubStorageFactory{
		errByName: map[string]error{"missing": errors.New("no backend")},
		client:    &mongo.Client{},
	}
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return fakeSF, nil
	}

	cfg := config.LoadConfig()
	cfg.Puller.Buffer.Path = t.TempDir()
	cfg.Puller.Backends = []puller_config.PullerBackendConfig{{Name: "missing"}}

	mgr := NewManager(cfg, Options{RunPuller: true})

	err := mgr.initPullerService(context.Background())
	assert.Error(t, err)
}

type fakeDocumentStore struct {
	db        *mongo.Database
	retention time.Duration
}

func (f *fakeDocumentStore) Get(ctx context.Context, database, path string, _ ...storage.ReadOptions) (*storage.StoredDoc, error) {
	return nil, nil
}
func (f *fakeDocumentStore) Create(ctx context.Context, database string, doc storage.StoredDoc) error {
	return nil
}
func (f *fakeDocumentStore) Update(ctx context.Context, database, path string, data map[string]interface{}, pred model.Filters) error {
	return nil
}
func (f *fakeDocumentStore) Patch(ctx context.Context, database, path string, data map[string]interface{}, pred model.Filters) error {
	return nil
}
func (f *fakeDocumentStore) Delete(ctx context.Context, database, path string, pred model.Filters) error {
	return nil
}
func (f *fakeDocumentStore) Query(ctx context.Context, database string, q model.Query) ([]*storage.StoredDoc, error) {
	return nil, nil
}
func (f *fakeDocumentStore) GetMany(ctx context.Context, database string, paths []string, _ ...storage.ReadOptions) ([]*storage.StoredDoc, error) {
	return nil, nil
}
func (f *fakeDocumentStore) Watch(ctx context.Context, database, collection string, after storage.WatchCheckpoint, opts storage.WatchOptions) (storage.WatchStream, error) {
	return nil, nil
}
func (f *fakeDocumentStore) Close(ctx context.Context) error { return nil }
func (f *fakeDocumentStore) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	return 0, nil
}

type fakeAuthStore struct {
	db           *mongo.Database
	ensureCalled bool
}

func (f *fakeAuthStore) CreateUser(ctx context.Context, user *storage.User) error {
	return nil
}
func (f *fakeAuthStore) GetUserByUsername(ctx context.Context, username string) (*storage.User, error) {
	return nil, identity.ErrUserNotFound
}
func (f *fakeAuthStore) GetUserByID(ctx context.Context, id string) (*storage.User, error) {
	return nil, identity.ErrUserNotFound
}
func (f *fakeAuthStore) ListUsers(ctx context.Context, limit int, offset int) ([]*storage.User, error) {
	return nil, nil
}
func (f *fakeAuthStore) UpdateUser(ctx context.Context, user *storage.User) error {
	return nil
}
func (f *fakeAuthStore) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	return nil
}
func (f *fakeAuthStore) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	return nil
}
func (f *fakeAuthStore) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	return nil
}
func (f *fakeAuthStore) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	return false, nil
}
func (f *fakeAuthStore) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	return nil
}
func (f *fakeAuthStore) EnsureIndexes(ctx context.Context) error {
	f.ensureCalled = true
	return nil
}
func (f *fakeAuthStore) Close(ctx context.Context) error { return nil }

type fakeDocumentProvider struct {
	store storage.DocumentStore
}

func (f *fakeDocumentProvider) Document() storage.DocumentStore { return f.store }
func (f *fakeDocumentProvider) Close(ctx context.Context) error { return nil }

type fakeAuthProvider struct {
	users       storage.UserStore
	revocations storage.TokenRevocationStore
}

func (f *fakeAuthProvider) Users() storage.UserStore                  { return f.users }
func (f *fakeAuthProvider) Revocations() storage.TokenRevocationStore { return f.revocations }
func (f *fakeAuthProvider) Close(ctx context.Context) error           { return nil }

type stubQueryService struct{}

func (s *stubQueryService) GetDocument(context.Context, string, string) (model.Document, error) {
	return model.Document{}, nil
}

func (s *stubQueryService) CreateDocument(context.Context, string, model.Document) error {
	return nil
}

func (s *stubQueryService) ReplaceDocument(context.Context, string, model.Document, model.Filters) (model.Document, error) {
	return model.Document{}, nil
}

func (s *stubQueryService) PatchDocument(context.Context, string, model.Document, model.Filters) (model.Document, error) {
	return model.Document{}, nil
}

func (s *stubQueryService) DeleteDocument(context.Context, string, string, model.Filters) error {
	return nil
}

func (s *stubQueryService) ExecuteQuery(context.Context, string, model.Query) ([]model.Document, error) {
	return nil, nil
}

func (s *stubQueryService) WatchCollection(context.Context, string, string) (<-chan storage.Event, error) {
	return nil, nil
}

func (s *stubQueryService) Pull(context.Context, string, storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	return nil, nil
}

func (s *stubQueryService) Push(context.Context, string, storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	return nil, nil
}

// stubIndexerService implements indexer.LocalService for testing
type stubIndexerService struct{}

func (s *stubIndexerService) Search(ctx context.Context, database string, plan indexer.Plan) ([]indexer.DocRef, error) {
	return nil, nil
}

func (s *stubIndexerService) Health(ctx context.Context) (indexer.Health, error) {
	return indexer.Health{}, nil
}

func (s *stubIndexerService) Stats(ctx context.Context) (indexer.Stats, error) {
	return indexer.Stats{}, nil
}

func (s *stubIndexerService) Start(ctx context.Context) error {
	return nil
}

func (s *stubIndexerService) Stop(ctx context.Context) error {
	return nil
}

func (s *stubIndexerService) ApplyEvent(ctx context.Context, evt *indexer.ChangeEvent, progress string) error {
	return nil
}

func (s *stubIndexerService) Manager() *indexer.IndexManager {
	return nil
}

func (s *stubIndexerService) InvalidateDatabase(ctx context.Context, database string) error {
	return nil
}

type stubStorageFactory struct {
	dbByName  map[string]string
	errByName map[string]error
	client    *mongo.Client
}

func (s *stubStorageFactory) Document() storage.DocumentStore          { return nil }
func (s *stubStorageFactory) User() storage.UserStore                  { return nil }
func (s *stubStorageFactory) Revocation() storage.TokenRevocationStore { return nil }
func (s *stubStorageFactory) Database() database.DatabaseStore         { return nil }
func (s *stubStorageFactory) GetMongoClient(name string) (*mongo.Client, string, error) {
	if err := s.errByName[name]; err != nil {
		return nil, "", err
	}

	db := s.dbByName[name]
	if db == "" {
		db = name
	}
	return s.client, db, nil
}
func (s *stubStorageFactory) Close() error { return nil }

type fakeStorageFactory struct {
	docStore storage.DocumentStore
	usrStore storage.UserStore
	revStore storage.TokenRevocationStore
	dbStore  database.DatabaseStore
}

func (f *fakeStorageFactory) Document() storage.DocumentStore          { return f.docStore }
func (f *fakeStorageFactory) User() storage.UserStore                  { return f.usrStore }
func (f *fakeStorageFactory) Revocation() storage.TokenRevocationStore { return f.revStore }
func (f *fakeStorageFactory) Database() database.DatabaseStore         { return f.dbStore }
func (f *fakeStorageFactory) GetMongoClient(name string) (*mongo.Client, string, error) {
	return nil, "", nil
}
func (f *fakeStorageFactory) Close() error { return nil }

type stubAuthN struct{}

func (s *stubAuthN) Middleware(next http.Handler) http.Handler         { return next }
func (s *stubAuthN) MiddlewareOptional(next http.Handler) http.Handler { return next }
func (s *stubAuthN) SignIn(ctx context.Context, req identity.LoginRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (s *stubAuthN) SignUp(ctx context.Context, req identity.SignupRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (s *stubAuthN) Refresh(ctx context.Context, req identity.RefreshRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (s *stubAuthN) ListUsers(ctx context.Context, limit int, offset int) ([]*identity.User, error) {
	return nil, nil
}
func (s *stubAuthN) UpdateUser(ctx context.Context, id string, roles []string, dbAdmin []string, disabled bool) error {
	return nil
}
func (s *stubAuthN) Logout(ctx context.Context, refreshToken string) error      { return nil }
func (s *stubAuthN) GenerateSystemToken(serviceName string) (string, error)     { return "", nil }
func (s *stubAuthN) ValidateToken(tokenString string) (*identity.Claims, error) { return nil, nil }

// stubAuthZ is a minimal AuthZ implementation for testing
type stubAuthZ struct{}

func (s *stubAuthZ) Evaluate(ctx context.Context, database string, path string, action string, req identity.AuthzRequest, existingRes *identity.Resource) (bool, error) {
	return true, nil
}
func (s *stubAuthZ) GetRules() *identity.RuleSet                           { return nil }
func (s *stubAuthZ) GetRulesForDatabase(database string) *identity.RuleSet { return nil }
func (s *stubAuthZ) UpdateRules(database string, content []byte) error     { return nil }
func (s *stubAuthZ) LoadRulesFromDir(dirPath string) error                 { return nil }

func TestManager_Init_StandaloneMode(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = ""
	cfg.Trigger.Evaluator.RulesPath = "" // Clear trigger rules for unit tests
	cfg.Puller.Backends = nil            // Clear puller backends for unit tests
	mgr := NewManager(cfg, Options{
		Mode:      ModeStandalone,
		RunAPI:    true,
		RunPuller: true,
	})

	// Set up mock puller service (required for standalone mode)
	mgr.pullerService = &mockPullerService{}

	err := mgr.Init(context.Background())
	assert.NoError(t, err)
	// In standalone mode, the Unified Server (server.Default()) handles all HTTP routing.
	// No separate HTTP servers are needed since API routes are registered
	// directly to the unified server mux.
	assert.NotNil(t, mgr.storageFactory)
}

func TestManager_Init_StandaloneMode_NoHTTPForCSP(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = ""
	cfg.Trigger.Evaluator.RulesPath = ""
	cfg.Puller.Backends = nil // Clear puller backends for unit tests
	mgr := NewManager(cfg, Options{
		Mode:      ModeStandalone,
		RunAPI:    true,
		RunPuller: true,
	})

	// Set up mock puller service (required for standalone mode)
	mgr.pullerService = &mockPullerService{}

	err := mgr.Init(context.Background())
	assert.NoError(t, err)
	// In standalone mode, CSP and Query services run in-process without separate HTTP servers.
	// The Unified Server (server.Default()) handles all HTTP routing.
}

func TestManager_initQueryService(t *testing.T) {
	fakeDocStore := &fakeDocumentStore{}
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Query.IndexerAddr = "localhost:9000"
	mgr := NewManager(cfg, Options{RunQuery: true, Mode: ModeDistributed})

	service, err := mgr.initQueryService(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, service)
}

// Note: TestManager_initQueryService_StandaloneMode and TestManager_initQueryService_StandaloneMissingIndexer
// were removed because standalone mode Query initialization is now inlined in initStandalone().
// See TestManager_Init_StandaloneMode and TestManager_initStandalone_QueryServiceError for standalone tests.

// Note: TestManager_initQueryService_DistributedMissingURL was removed because
// validation of IndexerAddr in distributed mode now happens at config load time
// via Config.Validate(). See TestConfig_Validate_DistributedMode_MissingAddresses
// in internal/config/config_test.go.

func TestManager_initQueryService_StorageError(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return nil, errors.New("storage connection failed")
	}

	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunQuery: true})

	_, err := mgr.initQueryService(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage connection failed")
}

func TestManager_initQueryGRPCServer(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Server.GRPCPort = 0
	mgr := NewManager(cfg, Options{})

	// Initialize the unified server first
	server.InitDefault(cfg.Server, nil)

	mockService := &stubQueryService{}
	mgr.initQueryGRPCServer(mockService)
	// gRPC server registration uses the unified server
	// Just verify it doesn't panic
}

func TestManager_initStandalone_APIServerError(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = "/nonexistent/rules/file.yaml"
	cfg.Puller.Backends = nil // Clear puller backends for unit tests
	mgr := NewManager(cfg, Options{
		Mode:      ModeStandalone,
		RunAPI:    true,
		RunPuller: true,
	})

	// Set up mock puller service (required for standalone mode)
	mgr.pullerService = &mockPullerService{}

	err := mgr.Init(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create authz engine")
}

func TestManager_initDistributed_APIServerError(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = "/nonexistent/rules/file.yaml"
	mgr := NewManager(cfg, Options{
		Mode:   ModeDistributed,
		RunAPI: true,
	})

	err := mgr.Init(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create authz engine")
}

func TestManager_Init_PullerServiceError(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &stubStorageFactory{
			// Return an error for the "missing_backend" backend
			errByName: map[string]error{"missing_backend": errors.New("backend not found")},
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Puller.Buffer.Path = t.TempDir()
	// Configure a backend that will fail during initialization
	cfg.Puller.Backends = []puller_config.PullerBackendConfig{{Name: "missing_backend"}}

	mgr := NewManager(cfg, Options{
		Mode:      ModeDistributed,
		RunPuller: true,
		RunQuery:  true, // Need this to trigger storage initialization
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := mgr.Init(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get mongo client for backend")
}

func TestManager_initDistributed_TriggerServicesError(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = ""
	// Configure NATS to fail connection
	cfg.Trigger.NatsURL = "nats://127.0.0.1:1"

	mgr := NewManager(cfg, Options{
		Mode:                ModeDistributed,
		RunTriggerEvaluator: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := mgr.Init(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to connect to NATS")
}

// mockPullerService for streamer testing
type mockPullerService struct{}

func (m *mockPullerService) Subscribe(ctx context.Context, consumerID string, after string) <-chan *puller.Event {
	return nil
}
func (m *mockPullerService) AddBackend(name string, client *mongo.Client, dbName string, cfg puller_config.PullerBackendConfig) error {
	return nil
}
func (m *mockPullerService) Start(context.Context) error { return nil }
func (m *mockPullerService) Stop(context.Context) error  { return nil }
func (m *mockPullerService) BackendNames() []string      { return nil }
func (m *mockPullerService) SetEventHandler(handler func(ctx context.Context, backendName string, event *puller.ChangeEvent) error) {
}
func (m *mockPullerService) Replay(ctx context.Context, after map[string]string, streaming bool) (puller.Iterator, error) {
	return nil, nil
}

// Note: TestManager_initStreamerService_Standalone was removed because
// standalone mode Streamer initialization is now inlined in initStandalone().
// See TestManager_Init_StandaloneMode for standalone tests.

func TestManager_initStreamerService_Distributed(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Server.GRPCPort = 0
	cfg.Streamer.Server.PullerAddr = "localhost:50051"

	// Initialize the unified server first
	server.InitDefault(cfg.Server, nil)

	mgr := NewManager(cfg, Options{Mode: ModeDistributed, RunStreamer: true})

	err := mgr.initStreamerService()
	assert.NoError(t, err)
	assert.NotNil(t, mgr.streamerService)
}

// Note: TestManager_initStreamerService_NoPuller was removed because
// validation of PullerAddr in distributed mode now happens at config load time
// via Config.Validate(). See TestConfig_Validate_DistributedMode_MissingAddresses
// in internal/config/config_test.go.

func TestManager_initGateway(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Gateway.QueryServiceURL = "localhost:50051"
	cfg.Gateway.StreamerServiceURL = "localhost:50051"
	cfg.Identity.AuthZ.RulesPath = ""

	// Initialize the unified server first
	server.InitDefault(cfg.Server, nil)

	mgr := NewManager(cfg, Options{Mode: ModeDistributed, RunAPI: true})
	mgr.authService = &stubAuthN{}

	err := mgr.initGateway()
	assert.NoError(t, err)
	assert.NotNil(t, mgr.streamerClient)
	assert.NotNil(t, mgr.rtServer)
}

func TestManager_initDistributed_RunStreamer(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Server.GRPCPort = 0
	cfg.Streamer.Server.PullerAddr = "localhost:50051"

	// Initialize the unified server first
	server.InitDefault(cfg.Server, nil)

	mgr := NewManager(cfg, Options{Mode: ModeDistributed, RunStreamer: true})

	err := mgr.initDistributed(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.streamerService)
}

func TestManager_initDistributed_RunQueryAndStreamer(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.GRPCPort = 0
	cfg.Streamer.Server.PullerAddr = "localhost:50051"

	// Initialize the unified server first
	server.InitDefault(cfg.Server, nil)

	mgr := NewManager(cfg, Options{
		Mode:        ModeDistributed,
		RunQuery:    true,
		RunStreamer: true,
	})

	err := mgr.initDistributed(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.streamerService)
}

func TestManager_initDistributed_AllServices(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.GRPCPort = 0
	cfg.Server.HTTPPort = 0
	cfg.Gateway.QueryServiceURL = "localhost:50051"
	cfg.Gateway.StreamerServiceURL = "localhost:50051"
	cfg.Streamer.Server.PullerAddr = "localhost:50051"
	cfg.Identity.AuthZ.RulesPath = ""

	// Initialize the unified server first
	server.InitDefault(cfg.Server, nil)

	mgr := NewManager(cfg, Options{
		Mode:        ModeDistributed,
		RunQuery:    true,
		RunStreamer: true,
		RunAPI:      true,
	})

	err := mgr.Init(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.streamerService) // Local Streamer service
	assert.NotNil(t, mgr.streamerClient)  // Gateway uses client
	assert.NotNil(t, mgr.rtServer)
}

func TestManager_initStandalone_TriggerServicesError(t *testing.T) {
	origFactory := storageFactoryFactory
	origEvaluatorFactory := evaluatorServiceFactory
	defer func() {
		storageFactoryFactory = origFactory
		evaluatorServiceFactory = origEvaluatorFactory
	}()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	// Mock evaluator factory to return error
	evaluatorServiceFactory = func(deps evaluator.Dependencies, cfg evaluator.Config) (evaluator.Service, error) {
		return nil, fmt.Errorf("evaluator service error")
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = ""
	cfg.Puller.Backends = nil            // Clear puller backends for unit tests
	cfg.Trigger.Evaluator.RulesPath = "" // No rules path to avoid file system dependency

	mgr := NewManager(cfg, Options{
		Mode:                ModeStandalone,
		RunAPI:              true,
		RunPuller:           true,
		RunTriggerEvaluator: true,
	})

	// Set up mock puller service (required for standalone mode)
	mgr.pullerService = &mockPullerService{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := mgr.Init(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "evaluator service error")
}

func TestManager_initStandalone_WithPuller(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeSF := &stubStorageFactory{
		dbByName: map[string]string{"default": "test_db"},
		client:   &mongo.Client{},
	}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return fakeSF, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = ""
	cfg.Trigger.Evaluator.RulesPath = ""
	cfg.Puller.Buffer.Path = t.TempDir()
	cfg.Puller.Backends = []puller_config.PullerBackendConfig{{Name: "default"}}

	mgr := NewManager(cfg, Options{
		Mode:      ModeStandalone,
		RunAPI:    true,
		RunPuller: true,
	})
	defer mgr.Shutdown(context.Background())

	err := mgr.Init(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.pullerService)
	// In standalone mode, pullerGRPC should be nil (no gRPC registration)
	assert.Nil(t, mgr.pullerGRPC)
}

func TestManager_initStandalone_PullerError(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return nil, errors.New("storage error")
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Puller.Backends = []puller_config.PullerBackendConfig{{Name: "default"}}

	mgr := NewManager(cfg, Options{
		Mode:      ModeStandalone,
		RunAPI:    true,
		RunPuller: true,
	})

	err := mgr.Init(context.Background())
	assert.Error(t, err)
}

func TestManager_initPullerGRPCServer(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Server.GRPCPort = 0
	cfg.Puller.Buffer.Path = t.TempDir()

	// Initialize the unified server first
	server.InitDefault(cfg.Server, nil)

	mgr := NewManager(cfg, Options{
		Mode:      ModeDistributed,
		RunPuller: true,
	})

	// Create a minimal puller service
	mgr.pullerService = puller.NewService(cfg.Puller, nil)

	// Call initPullerGRPCServer
	mgr.initPullerGRPCServer()

	assert.NotNil(t, mgr.pullerGRPC)
}

// Note: TestManager_initIndexerService_Standalone was removed because
// standalone mode Indexer initialization is now inlined in initStandalone().
// See TestManager_Init_StandaloneMode for standalone tests.

func TestManager_initIndexerService_Distributed(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Indexer.TemplatePath = ""
	cfg.Indexer.PullerAddr = "localhost:50051"

	mgr := NewManager(cfg, Options{
		Mode:       ModeDistributed,
		RunIndexer: true,
	})

	// Distributed mode uses gRPC client (doesn't need local pullerService)
	err := mgr.initIndexerService(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.indexerService)
}

func TestManager_initIndexerGRPCServer(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Server.GRPCPort = 0
	cfg.Indexer.TemplatePath = ""

	// Initialize the unified server first
	server.InitDefault(cfg.Server, nil)

	mgr := NewManager(cfg, Options{
		Mode:       ModeDistributed,
		RunIndexer: true,
	})

	// Create a real indexer service - simpler than mocking the internal interface
	svc, err := indexer.NewService(indexer_config.Config{}, nil, slog.Default())
	if err != nil {
		t.Fatalf("failed to create indexer service: %v", err)
	}
	mgr.indexerService = svc

	// Call initIndexerGRPCServer
	mgr.initIndexerGRPCServer()
	// Just verify it doesn't panic - gRPC registration happens on server.Default()
}

func TestManager_initStandalone_WithIndexer(t *testing.T) {
	origFactory := storageFactoryFactory
	origEvalFactory := evaluatorServiceFactory
	origDeliveryFactory := deliveryServiceFactory
	defer func() {
		storageFactoryFactory = origFactory
		evaluatorServiceFactory = origEvalFactory
		deliveryServiceFactory = origDeliveryFactory
	}()

	fakeSF := &stubStorageFactory{
		dbByName: map[string]string{"default": "test_db"},
		client:   &mongo.Client{},
	}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return fakeSF, nil
	}

	// Mock trigger services (standalone uses memory pubsub, so we just mock the evaluator/delivery services)
	evaluatorServiceFactory = func(deps evaluator.Dependencies, cfg evaluator.Config) (evaluator.Service, error) {
		return &fakeEvaluatorService{}, nil
	}
	deliveryServiceFactory = func(deps delivery.Dependencies, cfg delivery.Config) (delivery.Service, error) {
		return &fakeDeliveryService{}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = ""
	cfg.Indexer.TemplatePath = ""
	cfg.Puller.Buffer.Path = t.TempDir()
	cfg.Puller.Backends = []puller_config.PullerBackendConfig{{Name: "default"}}
	cfg.Trigger.Evaluator.RulesPath = "" // Mock factory handles this

	mgr := NewManager(cfg, Options{
		Mode:       ModeStandalone,
		RunAPI:     true,
		RunPuller:  true,
		RunIndexer: true,
	})
	defer func() {
		// Clear pubsub provider before shutdown to avoid panic on fake conn close
		mgr.pubsubProvider = nil
		mgr.Shutdown(context.Background())
	}()

	err := mgr.Init(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.indexerService)
}

func TestManager_initDistributed_WithIndexer(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeSF := &stubStorageFactory{
		dbByName: map[string]string{"default": "test_db"},
		client:   &mongo.Client{},
	}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return fakeSF, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Server.GRPCPort = 0
	cfg.Indexer.TemplatePath = ""
	cfg.Puller.Buffer.Path = t.TempDir()
	cfg.Puller.Backends = []puller_config.PullerBackendConfig{{Name: "default"}}

	// Initialize the unified server first
	server.InitDefault(cfg.Server, nil)

	mgr := NewManager(cfg, Options{
		Mode:       ModeDistributed,
		RunPuller:  true,
		RunIndexer: true,
	})
	defer mgr.Shutdown(context.Background())

	err := mgr.initDistributed(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.indexerService)
}

func TestManager_initStandalone_IndexerError(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Server.HTTPPort = 0
	cfg.Identity.AuthZ.RulesPath = ""
	cfg.Puller.Backends = nil
	// Use invalid storage mode to trigger indexer error
	cfg.Indexer.StorageMode = "invalid_storage_mode"

	mgr := NewManager(cfg, Options{
		Mode:      ModeStandalone,
		RunAPI:    true,
		RunPuller: true,
	})

	// Set up mock puller service (required for standalone mode)
	mgr.pullerService = &mockPullerService{}

	err := mgr.Init(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create indexer service")
}

// Note: TestManager_initStandalone_QueryServiceError was removed because
// standalone mode Query initialization is now inlined in initStandalone().
// The error path is tested via the initStandalone flow where missing Puller
// causes the entire initialization to fail early.

// Fake trigger services for standalone tests
type fakeEvaluatorService struct{}

func (f *fakeEvaluatorService) LoadTriggers(triggers []*trigger.Trigger) error { return nil }
func (f *fakeEvaluatorService) Start(ctx context.Context) error                { return nil }
func (f *fakeEvaluatorService) Close() error                                   { return nil }

type fakeDeliveryService struct{}

func (f *fakeDeliveryService) Start(ctx context.Context) error { return nil }

// Interface compliance checks
var _ evaluator.Service = (*fakeEvaluatorService)(nil)
var _ delivery.Service = (*fakeDeliveryService)(nil)
var _ pubsub.Publisher = pubsubtesting.NewMockPublisher()
var _ pubsub.Consumer = pubsubtesting.NewMockConsumer()

func TestManager_ensureAdminUser_Success(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeAuth := &fakeAuthStore{}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Identity.AuthN.PrivateKeyFile = filepath.Join(t.TempDir(), "auth.pem")
	cfg.Identity.Admin.Username = "syntrix"
	cfg.Identity.Admin.Password = "TestPassword123!"

	mgr := NewManager(cfg, Options{RunAPI: true})

	// Initialize auth service first
	err := mgr.initAuthService(context.Background())
	assert.NoError(t, err)

	// Now test ensureAdminUser
	err = mgr.ensureAdminUser(context.Background())
	assert.NoError(t, err)
}

func TestManager_ensureAdminUser_UserAlreadyExists(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	// Create a fake auth store that returns user exists
	fakeAuth := &fakeAuthStoreWithUser{
		users: map[string]*storage.User{
			"syntrix": {ID: "existing-id", Username: "syntrix"},
		},
	}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Identity.AuthN.PrivateKeyFile = filepath.Join(t.TempDir(), "auth.pem")
	cfg.Identity.Admin.Username = "syntrix"
	cfg.Identity.Admin.Password = "TestPassword123!"

	mgr := NewManager(cfg, Options{RunAPI: true})

	err := mgr.initAuthService(context.Background())
	assert.NoError(t, err)

	// Should not error when user already exists
	err = mgr.ensureAdminUser(context.Background())
	assert.NoError(t, err)
}

func TestManager_ensureAdminUser_NoAuthService(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Identity.Admin.Username = "syntrix"
	cfg.Identity.Admin.Password = "TestPassword123!"

	mgr := NewManager(cfg, Options{})
	// authService is nil

	// Should skip gracefully when no auth service
	err := mgr.ensureAdminUser(context.Background())
	assert.NoError(t, err)
}

func TestManager_ensureAdminUser_NoUsername(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Identity.Admin.Username = ""
	cfg.Identity.Admin.Password = "TestPassword123!"

	mgr := NewManager(cfg, Options{RunAPI: true})
	mgr.authService = &stubAuthN{}

	// Should skip gracefully when no username configured
	err := mgr.ensureAdminUser(context.Background())
	assert.NoError(t, err)
}

func TestManager_ensureAdminUser_NoPassword(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Identity.Admin.Username = "syntrix"
	cfg.Identity.Admin.Password = ""

	mgr := NewManager(cfg, Options{RunAPI: true})
	mgr.authService = &stubAuthN{}

	// Should skip gracefully when no password configured
	err := mgr.ensureAdminUser(context.Background())
	assert.NoError(t, err)
}

// fakeAuthStoreWithUser is a variant of fakeAuthStore that can return existing users
type fakeAuthStoreWithUser struct {
	users map[string]*storage.User
}

func (f *fakeAuthStoreWithUser) CreateUser(ctx context.Context, user *storage.User) error {
	if f.users == nil {
		f.users = make(map[string]*storage.User)
	}
	f.users[user.Username] = user
	return nil
}
func (f *fakeAuthStoreWithUser) GetUserByUsername(ctx context.Context, username string) (*storage.User, error) {
	if user, ok := f.users[username]; ok {
		return user, nil
	}
	return nil, identity.ErrUserNotFound
}
func (f *fakeAuthStoreWithUser) GetUserByID(ctx context.Context, id string) (*storage.User, error) {
	return nil, identity.ErrUserNotFound
}
func (f *fakeAuthStoreWithUser) ListUsers(ctx context.Context, limit int, offset int) ([]*storage.User, error) {
	return nil, nil
}
func (f *fakeAuthStoreWithUser) UpdateUser(ctx context.Context, user *storage.User) error {
	return nil
}
func (f *fakeAuthStoreWithUser) UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error {
	return nil
}
func (f *fakeAuthStoreWithUser) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	return nil
}
func (f *fakeAuthStoreWithUser) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	return nil
}
func (f *fakeAuthStoreWithUser) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	return false, nil
}
func (f *fakeAuthStoreWithUser) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	return nil
}
func (f *fakeAuthStoreWithUser) EnsureIndexes(ctx context.Context) error {
	return nil
}
func (f *fakeAuthStoreWithUser) Close(ctx context.Context) error { return nil }

func TestManager_ensureAdminUser_CreatesUserWithAdminRole(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	// Use fakeAuthStoreWithUser to track created users
	fakeAuth := &fakeAuthStoreWithUser{
		users: make(map[string]*storage.User),
	}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			usrStore: fakeAuth,
			revStore: fakeAuth,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Identity.AuthN.PrivateKeyFile = filepath.Join(t.TempDir(), "auth.pem")
	cfg.Identity.Admin.Username = "syntrix"
	cfg.Identity.Admin.Password = "TestPassword123!"

	mgr := NewManager(cfg, Options{RunAPI: true})

	// Initialize auth service first
	err := mgr.initAuthService(context.Background())
	assert.NoError(t, err)

	// Ensure admin user is created
	err = mgr.ensureAdminUser(context.Background())
	assert.NoError(t, err)

	// Verify the user was created in the store
	createdUser, err := fakeAuth.GetUserByUsername(context.Background(), "syntrix")
	assert.NoError(t, err)
	assert.NotNil(t, createdUser)
	assert.Equal(t, "syntrix", createdUser.Username)

	// Verify admin role is assigned
	assert.Contains(t, createdUser.Roles, "admin", "syntrix user should have admin role")

	// Verify user ID is set
	assert.NotEmpty(t, createdUser.ID)

	// Verify password is hashed (not stored as plain text)
	assert.NotEqual(t, "TestPassword123!", createdUser.PasswordHash)
	assert.NotEmpty(t, createdUser.PasswordHash)
}

// fakeDatabaseStore is a mock implementation of database.DatabaseStore for testing
type fakeDatabaseStore struct {
	databases map[string]*database.Database
}

func newFakeDatabaseStore() *fakeDatabaseStore {
	return &fakeDatabaseStore{
		databases: make(map[string]*database.Database),
	}
}

func (f *fakeDatabaseStore) Create(ctx context.Context, db *database.Database) error {
	f.databases[db.ID] = db
	return nil
}

func (f *fakeDatabaseStore) Get(ctx context.Context, id string) (*database.Database, error) {
	if db, ok := f.databases[id]; ok {
		return db, nil
	}
	return nil, database.ErrDatabaseNotFound
}

func (f *fakeDatabaseStore) GetBySlug(ctx context.Context, slug string) (*database.Database, error) {
	for _, db := range f.databases {
		if db.Slug != nil && *db.Slug == slug {
			return db, nil
		}
	}
	return nil, database.ErrDatabaseNotFound
}

func (f *fakeDatabaseStore) List(ctx context.Context, opts database.ListOptions) ([]*database.Database, int, error) {
	var result []*database.Database
	for _, db := range f.databases {
		result = append(result, db)
	}
	return result, len(result), nil
}

func (f *fakeDatabaseStore) Update(ctx context.Context, db *database.Database) error {
	if _, ok := f.databases[db.ID]; !ok {
		return database.ErrDatabaseNotFound
	}
	f.databases[db.ID] = db
	return nil
}

func (f *fakeDatabaseStore) Delete(ctx context.Context, id string) error {
	if _, ok := f.databases[id]; !ok {
		return database.ErrDatabaseNotFound
	}
	delete(f.databases, id)
	return nil
}

func (f *fakeDatabaseStore) CountByOwner(ctx context.Context, ownerID string) (int, error) {
	count := 0
	for _, db := range f.databases {
		if db.OwnerID == ownerID {
			count++
		}
	}
	return count, nil
}

func (f *fakeDatabaseStore) Exists(ctx context.Context, id string) (bool, error) {
	_, ok := f.databases[id]
	return ok, nil
}

func (f *fakeDatabaseStore) Close(ctx context.Context) error {
	return nil
}

func TestManager_initDatabaseService_WithDatabaseStore(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	fakeDbStore := newFakeDatabaseStore()
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
			dbStore:  fakeDbStore,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Database.MaxDatabasesPerUser = 10
	mgr := NewManager(cfg, Options{RunAPI: true})

	// Initialize storage factory first
	_, err := mgr.getStorageFactory(context.Background())
	assert.NoError(t, err)

	// Call initDatabaseService
	err = mgr.initDatabaseService(context.Background())
	assert.NoError(t, err)

	// Verify database service was initialized
	assert.NotNil(t, mgr.databaseService)
}

func TestManager_initDatabaseService_WithGatewayServer(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDocStore := &fakeDocumentStore{}
	fakeAuth := &fakeAuthStore{}
	fakeDbStore := newFakeDatabaseStore()
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: fakeDocStore,
			usrStore: fakeAuth,
			revStore: fakeAuth,
			dbStore:  fakeDbStore,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Database.MaxDatabasesPerUser = 5
	cfg.Identity.AuthZ.RulesPath = ""
	mgr := NewManager(cfg, Options{RunAPI: true})

	// Initialize storage factory first
	_, err := mgr.getStorageFactory(context.Background())
	assert.NoError(t, err)

	// Create a mock gateway server - simulate having gateway already initialized
	// We'll use the real gateway.NewServer but with mocks
	mockQuery := &stubQueryService{}
	mockAuth := &stubAuthN{}
	mockAuthz := &stubAuthZ{}
	mgr.gatewayServer, err = gateway.NewServer(mockQuery, mockAuth, mockAuthz, nil)
	assert.NoError(t, err)

	// Call initDatabaseService
	err = mgr.initDatabaseService(context.Background())
	assert.NoError(t, err)

	// Verify database service was initialized
	assert.NotNil(t, mgr.databaseService)
	// The SetDatabaseService should have been called on the gateway server
	// (we can't easily verify this without more complex mocking, but the coverage shows it was called)
}

func TestManager_ensureDefaultDatabase_NoAuthService(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Identity.Admin.Username = "syntrix"

	mgr := NewManager(cfg, Options{})
	// authService is nil

	// Should skip gracefully when no auth service
	err := mgr.ensureDefaultDatabase(context.Background())
	assert.NoError(t, err)
}

func TestManager_ensureDefaultDatabase_NoAdminUsername(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Identity.Admin.Username = ""

	mgr := NewManager(cfg, Options{RunAPI: true})
	mgr.authService = &stubAuthN{}

	// Should skip gracefully when no admin username configured
	err := mgr.ensureDefaultDatabase(context.Background())
	assert.NoError(t, err)
}

func TestManager_ensureDefaultDatabase_StorageFactoryError(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return nil, errors.New("storage connection failed")
	}

	cfg := config.LoadConfig()
	cfg.Identity.Admin.Username = "syntrix"
	mgr := NewManager(cfg, Options{RunAPI: true})
	mgr.authService = &stubAuthN{}

	err := mgr.ensureDefaultDatabase(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage connection failed")
}

func TestManager_ensureDefaultDatabase_NoDatabaseStore(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	// Return a storage factory with nil database store
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: &fakeDocumentStore{},
			usrStore: &fakeAuthStore{},
			revStore: &fakeAuthStore{},
			dbStore:  nil, // No database store
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Identity.Admin.Username = "syntrix"
	mgr := NewManager(cfg, Options{RunAPI: true})
	mgr.authService = &stubAuthN{}

	// Should skip gracefully when database store not available
	err := mgr.ensureDefaultDatabase(context.Background())
	assert.NoError(t, err)
}

func TestManager_ensureDefaultDatabase_Success(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDbStore := newFakeDatabaseStore()
	fakeUserStore := &fakeAuthStoreWithUser{
		users: map[string]*storage.User{
			"syntrix": {ID: "admin-user-id", Username: "syntrix"},
		},
	}
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: &fakeDocumentStore{},
			usrStore: fakeUserStore,
			revStore: fakeUserStore,
			dbStore:  fakeDbStore,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Identity.Admin.Username = "syntrix"
	mgr := NewManager(cfg, Options{RunAPI: true})
	mgr.authService = &stubAuthN{}

	err := mgr.ensureDefaultDatabase(context.Background())
	assert.NoError(t, err)

	// Verify that a default database was created
	db, err := fakeDbStore.GetBySlug(context.Background(), "default")
	assert.NoError(t, err)
	assert.NotNil(t, db)
	assert.Equal(t, "admin-user-id", db.OwnerID)
}

func TestManager_initDeletionWorker_Disabled(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Database.Deletion.Enabled = false

	mgr := NewManager(cfg, Options{RunQuery: true})

	// Should skip gracefully when deletion worker is disabled
	err := mgr.initDeletionWorker(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, mgr.deletionWorker)
}

func TestManager_initDeletionWorker_StorageFactoryError(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return nil, errors.New("storage connection failed")
	}

	cfg := config.LoadConfig()
	cfg.Database.Deletion.Enabled = true
	mgr := NewManager(cfg, Options{RunQuery: true})

	err := mgr.initDeletionWorker(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage connection failed")
}

func TestManager_initDeletionWorker_NoDatabaseStore(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	// Return a storage factory with nil database store
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: &fakeDocumentStore{},
			usrStore: &fakeAuthStore{},
			revStore: &fakeAuthStore{},
			dbStore:  nil, // No database store
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Database.Deletion.Enabled = true
	mgr := NewManager(cfg, Options{RunQuery: true})

	// Should skip gracefully when database store not available
	err := mgr.initDeletionWorker(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, mgr.deletionWorker)
}

func TestManager_initDeletionWorker_Success(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDbStore := newFakeDatabaseStore()
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: &fakeDocumentStore{},
			usrStore: &fakeAuthStore{},
			revStore: &fakeAuthStore{},
			dbStore:  fakeDbStore,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Database.Deletion.Enabled = true
	cfg.Database.Deletion.Interval = 5 * time.Minute
	cfg.Database.Deletion.BatchSize = 100
	cfg.Database.Deletion.MaxBatchesPerCycle = 10
	mgr := NewManager(cfg, Options{RunQuery: true})

	// Set up a mock indexer service (optional for deletion worker)
	mgr.indexerService = &stubIndexerService{}

	err := mgr.initDeletionWorker(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.deletionWorker)
}

func TestManager_initDeletionWorker_WithoutIndexer(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	fakeDbStore := newFakeDatabaseStore()
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: &fakeDocumentStore{},
			usrStore: &fakeAuthStore{},
			revStore: &fakeAuthStore{},
			dbStore:  fakeDbStore,
		}, nil
	}

	cfg := config.LoadConfig()
	cfg.Database.Deletion.Enabled = true
	mgr := NewManager(cfg, Options{RunQuery: true})

	// No indexer service - deletion worker should still work
	mgr.indexerService = nil

	err := mgr.initDeletionWorker(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.deletionWorker)
}

func TestManager_initDatabaseService_NoDatabaseStore(t *testing.T) {
	origFactory := storageFactoryFactory
	defer func() { storageFactoryFactory = origFactory }()

	// Return a storage factory with nil database store
	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{
			docStore: &fakeDocumentStore{},
			usrStore: &fakeAuthStore{},
			revStore: &fakeAuthStore{},
			dbStore:  nil, // No database store
		}, nil
	}

	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunAPI: true})

	// Initialize storage factory first
	_, err := mgr.getStorageFactory(context.Background())
	assert.NoError(t, err)

	// Should skip gracefully when database store not available
	err = mgr.initDatabaseService(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, mgr.databaseService)
}
