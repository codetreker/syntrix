package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/syntrixbase/syntrix/internal/config"
	"github.com/syntrixbase/syntrix/internal/core/pubsub"
	"github.com/syntrixbase/syntrix/internal/core/pubsub/memory"
	pubsubtesting "github.com/syntrixbase/syntrix/internal/core/pubsub/testing"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller"
	puller_config "github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/trigger"
	"github.com/syntrixbase/syntrix/internal/trigger/delivery"
	"github.com/syntrixbase/syntrix/internal/trigger/evaluator"
)

// mockTriggerPuller implements puller.LocalService for testing
type mockTriggerPuller struct{}

func (m *mockTriggerPuller) Subscribe(ctx context.Context, consumerID string, after string) <-chan *puller.Event {
	return nil
}
func (m *mockTriggerPuller) AddBackend(name string, client *mongo.Client, dbName string, cfg puller_config.PullerBackendConfig) error {
	return nil
}
func (m *mockTriggerPuller) Start(context.Context) error { return nil }
func (m *mockTriggerPuller) Stop(context.Context) error  { return nil }
func (m *mockTriggerPuller) BackendNames() []string      { return nil }
func (m *mockTriggerPuller) SetEventHandler(handler func(ctx context.Context, backendName string, event *puller.ChangeEvent) error) {
}
func (m *mockTriggerPuller) Replay(ctx context.Context, after map[string]string, streaming bool) (puller.Iterator, error) {
	return nil, nil
}
func (m *mockTriggerPuller) ReplayFromAdmission(ctx context.Context, after map[string]string, firstBroadcast map[string]puller.ClusterTime) (puller.Iterator, error) {
	return nil, nil
}

// setupTriggerTestFactories saves and restores all trigger-related factories
func setupTriggerTestFactories(t *testing.T) func() {
	origStorageFactory := storageFactoryFactory
	origEvalFactory := evaluatorServiceFactory
	origDeliveryFactory := deliveryServiceFactory
	origProviderFactory := pubsubProviderFactory

	return func() {
		storageFactoryFactory = origStorageFactory
		evaluatorServiceFactory = origEvalFactory
		deliveryServiceFactory = origDeliveryFactory
		pubsubProviderFactory = origProviderFactory
	}
}

func TestManager_InitTriggerServices_EvaluatorSuccess(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Trigger.Evaluator.RulesPath = ""
	mgr := NewManager(cfg, Options{Mode: ModeStandalone})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Set up fake puller service
	mgr.pullerService = &mockTriggerPuller{}

	// Mock evaluator factory
	evaluatorServiceFactory = func(deps evaluator.Dependencies, cfg evaluator.Config) (evaluator.Service, error) {
		return &fakeEvaluator{}, nil
	}

	// Mock delivery factory (standalone initializes both services)
	deliveryServiceFactory = func(deps delivery.Dependencies, cfg delivery.Config) (delivery.Service, error) {
		return &fakeConsumer{}, nil
	}

	err := mgr.initTriggerServicesStandalone(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.triggerService)
	assert.NotNil(t, mgr.triggerConsumer)
	// Verify memory pubsub was created
	assert.NotNil(t, mgr.pubsubProvider)
}

func TestManager_InitTriggerServices_DeliverySuccess(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunTriggerWorker: true, Mode: ModeDistributed})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Mock pubsub provider factory for distributed mode
	mockProvider := pubsubtesting.NewMockProvider()
	pubsubProviderFactory = func(url string) (pubsub.Provider, error) {
		return mockProvider, nil
	}

	// Mock delivery factory
	deliveryServiceFactory = func(deps delivery.Dependencies, cfg delivery.Config) (delivery.Service, error) {
		return &fakeConsumer{}, nil
	}

	err := mgr.initTriggerServices(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.triggerConsumer)
}

func TestManager_InitTriggerServices_BothServices(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Trigger.Evaluator.RulesPath = ""
	mgr := NewManager(cfg, Options{Mode: ModeStandalone})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Set up fake puller service
	mgr.pullerService = &mockTriggerPuller{}

	// Mock factories
	evaluatorServiceFactory = func(deps evaluator.Dependencies, cfg evaluator.Config) (evaluator.Service, error) {
		return &fakeEvaluator{}, nil
	}
	deliveryServiceFactory = func(deps delivery.Dependencies, cfg delivery.Config) (delivery.Service, error) {
		return &fakeConsumer{}, nil
	}

	err := mgr.initTriggerServicesStandalone(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.triggerService)
	assert.NotNil(t, mgr.triggerConsumer)
}

func TestManager_InitTriggerServices_EvaluatorNoPuller_Standalone(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{Mode: ModeStandalone})

	restore := setupTriggerTestFactories(t)
	defer restore()

	// No puller service set - should fail in standalone mode (checked before pubsub init)
	err := mgr.initTriggerServicesStandalone(context.Background())
	assert.ErrorContains(t, err, "puller service is required for trigger evaluator in standalone mode")
}

func TestManager_InitTriggerServices_EvaluatorError(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{Mode: ModeStandalone})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Set up fake puller service
	mgr.pullerService = &mockTriggerPuller{}

	// Mock evaluator factory to return error
	evaluatorServiceFactory = func(deps evaluator.Dependencies, cfg evaluator.Config) (evaluator.Service, error) {
		return nil, fmt.Errorf("evaluator error")
	}

	err := mgr.initTriggerServicesStandalone(context.Background())
	assert.ErrorContains(t, err, "evaluator error")
}

func TestManager_InitTriggerServices_DeliveryError(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Trigger.Evaluator.PullerAddr = "localhost:50051" // For distributed mode
	mgr := NewManager(cfg, Options{RunTriggerWorker: true, Mode: ModeDistributed})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Mock pubsub provider factory for distributed mode
	mockProvider := pubsubtesting.NewMockProvider()
	pubsubProviderFactory = func(url string) (pubsub.Provider, error) {
		return mockProvider, nil
	}

	// Mock delivery factory to return error
	deliveryServiceFactory = func(deps delivery.Dependencies, cfg delivery.Config) (delivery.Service, error) {
		return nil, fmt.Errorf("delivery error")
	}

	err := mgr.initTriggerServices(context.Background())
	assert.ErrorContains(t, err, "delivery error")
}

func TestManager_InitTriggerServices_ProviderError(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunTriggerEvaluator: true, Mode: ModeDistributed})

	restore := setupTriggerTestFactories(t)
	defer restore()

	// Mock pubsub provider factory to return error
	pubsubProviderFactory = func(url string) (pubsub.Provider, error) {
		return nil, fmt.Errorf("provider error")
	}

	err := mgr.initTriggerServices(context.Background())
	assert.ErrorContains(t, err, "provider error")
}

func TestManager_InitTriggerServices_WithRulesPath(t *testing.T) {
	cfg := config.LoadConfig()
	tmpDir := t.TempDir()
	yamlRules := `database: default
triggers:
  t1:
    collection: users
    events:
      - create
    url: http://localhost:8080/webhook
`
	assert.NoError(t, os.WriteFile(filepath.Join(tmpDir, "default.yml"), []byte(yamlRules), 0644))
	cfg.Trigger.Evaluator.RulesPath = tmpDir

	mgr := NewManager(cfg, Options{Mode: ModeStandalone})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Set up fake puller service
	mgr.pullerService = &mockTriggerPuller{}

	// Mock evaluator factory - verify rules path is passed
	evaluatorServiceFactory = func(deps evaluator.Dependencies, cfg evaluator.Config) (evaluator.Service, error) {
		assert.Equal(t, tmpDir, cfg.RulesPath)
		return &fakeEvaluator{}, nil
	}

	// Mock delivery factory (standalone initializes both services)
	deliveryServiceFactory = func(deps delivery.Dependencies, cfg delivery.Config) (delivery.Service, error) {
		return &fakeConsumer{}, nil
	}

	err := mgr.initTriggerServicesStandalone(context.Background())
	assert.NoError(t, err)
}

func TestManager_InitTriggerServices_PublisherError(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Trigger.Evaluator.RulesPath = ""
	// Use distributed mode with RunTriggerEvaluator to test publisher factory
	mgr := NewManager(cfg, Options{Mode: ModeDistributed, RunTriggerEvaluator: true})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Mock pubsub provider to return error on NewPublisher
	mockProvider := pubsubtesting.NewMockProvider()
	mockProvider.SetPublisherError(fmt.Errorf("publisher factory error"))
	pubsubProviderFactory = func(url string) (pubsub.Provider, error) {
		return mockProvider, nil
	}

	err := mgr.initTriggerServices(context.Background())
	assert.ErrorContains(t, err, "publisher factory error")
}

func TestManager_InitTriggerServices_ConsumerError(t *testing.T) {
	cfg := config.LoadConfig()
	mgr := NewManager(cfg, Options{RunTriggerWorker: true, Mode: ModeDistributed})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Mock pubsub provider to return error on NewConsumer
	mockProvider := pubsubtesting.NewMockProvider()
	mockProvider.SetConsumerError(fmt.Errorf("consumer factory error"))
	pubsubProviderFactory = func(url string) (pubsub.Provider, error) {
		return mockProvider, nil
	}

	err := mgr.initTriggerServices(context.Background())
	assert.ErrorContains(t, err, "consumer factory error")
}

func TestManager_InitTriggerServices_Distributed_EvaluatorSuccess(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Trigger.Evaluator.PullerAddr = "localhost:9000"
	cfg.Trigger.Evaluator.RulesPath = ""
	mgr := NewManager(cfg, Options{RunTriggerEvaluator: true, Mode: ModeDistributed})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Mock pubsub provider factory for distributed mode
	mockProvider := pubsubtesting.NewMockProvider()
	pubsubProviderFactory = func(url string) (pubsub.Provider, error) {
		return mockProvider, nil
	}

	// Mock evaluator factory
	evaluatorServiceFactory = func(deps evaluator.Dependencies, cfg evaluator.Config) (evaluator.Service, error) {
		return &fakeEvaluator{}, nil
	}

	err := mgr.initTriggerServices(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, mgr.triggerService)
}

func TestManager_InitTriggerServices_Distributed_EvaluatorPublisherError(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Trigger.Evaluator.PullerAddr = "localhost:9000"
	cfg.Trigger.Evaluator.RulesPath = ""
	mgr := NewManager(cfg, Options{RunTriggerEvaluator: true, Mode: ModeDistributed})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Mock pubsub provider to return error on NewPublisher
	mockProvider := pubsubtesting.NewMockProvider()
	mockProvider.SetPublisherError(fmt.Errorf("publisher error in distributed mode"))
	pubsubProviderFactory = func(url string) (pubsub.Provider, error) {
		return mockProvider, nil
	}

	err := mgr.initTriggerServices(context.Background())
	assert.ErrorContains(t, err, "publisher error in distributed mode")
}

func TestManager_InitTriggerServices_Distributed_EvaluatorServiceError(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Trigger.Evaluator.PullerAddr = "localhost:9000"
	cfg.Trigger.Evaluator.RulesPath = ""
	mgr := NewManager(cfg, Options{RunTriggerEvaluator: true, Mode: ModeDistributed})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Mock pubsub provider factory for distributed mode
	mockProvider := pubsubtesting.NewMockProvider()
	pubsubProviderFactory = func(url string) (pubsub.Provider, error) {
		return mockProvider, nil
	}

	// Mock evaluator factory to return error
	evaluatorServiceFactory = func(deps evaluator.Dependencies, cfg evaluator.Config) (evaluator.Service, error) {
		return nil, fmt.Errorf("evaluator service error in distributed mode")
	}

	err := mgr.initTriggerServices(context.Background())
	assert.ErrorContains(t, err, "evaluator service error in distributed mode")
}

func TestManager_InitTriggerServices_Standalone_UsesMemoryPubsub(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.Trigger.Evaluator.RulesPath = ""
	mgr := NewManager(cfg, Options{Mode: ModeStandalone})

	restore := setupTriggerTestFactories(t)
	defer restore()

	storageFactoryFactory = func(ctx context.Context, cfg *config.Config) (storage.StorageFactory, error) {
		return &fakeStorageFactory{}, nil
	}

	// Set up fake puller service
	mgr.pullerService = &mockTriggerPuller{}

	// Mock evaluator and delivery factories
	evaluatorServiceFactory = func(deps evaluator.Dependencies, cfg evaluator.Config) (evaluator.Service, error) {
		return &fakeEvaluator{}, nil
	}
	deliveryServiceFactory = func(deps delivery.Dependencies, cfg delivery.Config) (delivery.Service, error) {
		return &fakeConsumer{}, nil
	}

	err := mgr.initTriggerServicesStandalone(context.Background())
	assert.NoError(t, err)

	// Verify that memory pubsub was used
	_, ok := mgr.pubsubProvider.(*memory.Engine)
	assert.True(t, ok, "expected memory.Engine pubsub provider in standalone mode")
}

// Fakes

type fakeEvaluator struct{}

func (f *fakeEvaluator) LoadTriggers(triggers []*trigger.Trigger) error {
	return nil
}

func (f *fakeEvaluator) Start(ctx context.Context) error {
	return nil
}

func (f *fakeEvaluator) Close() error {
	return nil
}

type fakeConsumer struct{}

func (f *fakeConsumer) Start(ctx context.Context) error {
	return nil
}

type fakePublisher struct{ created bool }

func (f *fakePublisher) Publish(context.Context, *trigger.DeliveryTask) error {
	f.created = true
	return nil
}

func (f *fakePublisher) Close() {}
