package services

import (
	"context"
	"sync"
	"time"

	"github.com/syntrixbase/syntrix/internal/config"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/core/pubsub"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/gateway"
	"github.com/syntrixbase/syntrix/internal/gateway/realtime"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/puller"
	services_config "github.com/syntrixbase/syntrix/internal/services/config"
	"github.com/syntrixbase/syntrix/internal/streamer"
)

type Options struct {
	RunAPI              bool
	RunQuery            bool
	RunStreamer         bool
	RunTriggerEvaluator bool
	RunTriggerWorker    bool
	RunPuller           bool
	RunIndexer          bool
	BootstrapIndexes    bool
	WritesQuiesced      bool
	ResetDerived        bool
	BootstrapTimeout    time.Duration

	// Mode specifies the deployment mode (distributed or standalone).
	Mode services_config.DeploymentMode
}

// Re-export DeploymentMode constants for backwards compatibility.
const (
	ModeDistributed = services_config.ModeDistributed
	ModeStandalone  = services_config.ModeStandalone
)

type triggerService interface {
	Start(ctx context.Context) error
}

type triggerConsumer interface {
	Start(ctx context.Context) error
}

// deletionWorkerService defines the interface for the deletion worker
type deletionWorkerService interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type Manager struct {
	cfg  *config.Config
	opts Options

	storageFactory     storage.StorageFactory
	storageFactoryOnce sync.Once
	storageFactoryErr  error

	authService           identity.AuthN
	gatewayServer         *gateway.Server
	rtServer              *realtime.Server
	streamerService       streamer.StreamerServer // local Streamer service (when RunStreamer=true)
	streamerClient        streamer.Service        // remote Streamer client (for Gateway in distributed mode)
	triggerConsumer       triggerConsumer
	triggerService        triggerService
	pubsubProvider        pubsub.Provider // Unified pubsub provider (NATS or memory)
	pullerService         puller.LocalService
	pullerGRPC            *puller.GRPCServer
	indexerService        indexer.LocalService
	pullerStarted         bool
	indexerStarted        bool
	serverStarted         bool
	pullerGRPCInitialized bool
	bootstrapReady        bool

	// Database management
	databaseService database.Service
	deletionWorker  deletionWorkerService

	wg sync.WaitGroup
}

func NewManager(cfg *config.Config, opts Options) *Manager {
	if opts.BootstrapIndexes {
		opts.RunPuller = true
		opts.RunIndexer = true
	}
	return &Manager{
		cfg:  cfg,
		opts: opts,
	}
}

func (m *Manager) AuthService() identity.AuthN {
	return m.authService
}

func (m *Manager) DatabaseService() database.Service {
	return m.databaseService
}
