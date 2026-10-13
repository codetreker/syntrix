package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	deploymentconfig "github.com/codetreker/syntrix/deployment/config"
	"github.com/codetreker/syntrix/internal/config"
	"github.com/codetreker/syntrix/internal/core/database"
	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/gateway"
	"github.com/codetreker/syntrix/internal/gateway/realtime"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/indexer"
	"github.com/codetreker/syntrix/internal/initialization"
	"github.com/codetreker/syntrix/internal/puller"
	services_config "github.com/codetreker/syntrix/internal/services/config"
	"github.com/codetreker/syntrix/internal/streamer"
)

type Options struct {
	RunAPI                   bool
	RunQuery                 bool
	RunStreamer              bool
	RunTriggerEvaluator      bool
	RunTriggerWorker         bool
	RunPuller                bool
	RunIndexer               bool
	InitializationAcceptance bool
	BootstrapIndexes         bool
	WritesQuiesced           bool
	ResetDerived             bool
	BootstrapTimeout         time.Duration

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

type identityModule interface {
	Accounts() identity.AccountService
	Verifier() *identity.Verifier
	SystemTokenIssuer() identity.SystemTokenIssuer
	EnsureAdmin(context.Context) error
	ResolveOwner(context.Context, string) (string, string, error)
}

type Manager struct {
	cfg  *config.Config
	opts Options

	backends       *storage.Backends
	backendsOnce   sync.Once
	backendsErr    error
	identityModule identityModule
	shutdownMu     sync.Mutex
	serverStopped  bool

	storageFactory     storage.StorageFactory
	storageFactoryOnce sync.Once
	storageFactoryErr  error

	accountService        identity.AccountService
	tokenVerifier         identity.TokenVerifier
	systemTokenIssuer     identity.SystemTokenIssuer
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

	wg             sync.WaitGroup
	lifecycleMu    sync.Mutex
	lifeCtx        context.Context
	lifeCancel     context.CancelCauseFunc
	terminalErr    error
	initialization initializationCoordinator
	health         *runtimeHealth
	ownedClients   []io.Closer
}

func NewManager(cfg *config.Config, opts Options) *Manager {
	if opts.BootstrapIndexes {
		opts.RunPuller = true
		opts.RunIndexer = true
	}
	cfg.Services.ApplyDefaults()
	return &Manager{
		cfg:  cfg,
		opts: opts,
	}
}

func (m *Manager) SystemTokenIssuer() identity.SystemTokenIssuer {
	return m.systemTokenIssuer
}

func (m *Manager) DatabaseService() database.Service {
	return m.databaseService
}

func (m *Manager) writersHeld() bool {
	return m.opts.BootstrapIndexes || m.opts.InitializationAcceptance
}

func (m *Manager) lifecycle(ctx context.Context) context.Context {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.lifeCtx == nil {
		m.lifeCtx, m.lifeCancel = context.WithCancelCause(ctx)
	}
	return m.lifeCtx
}

func (m *Manager) fail(role string, cause error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.lifeCtx != nil && m.lifeCtx.Err() != nil {
		return
	}
	if cause == nil {
		cause = errors.New("background task returned unexpectedly")
	}
	m.terminalErr = errors.Join(m.terminalErr, &runtimeFailure{role: role, cause: cause})
	slog.Error("Runtime task failed", m.diagnosticAttrs(role, "running", "terminal_failure")...)
	if m.lifeCancel != nil {
		m.lifeCancel(m.terminalErr)
	}
}

func (m *Manager) Wait(ctx context.Context) error {
	m.lifecycleMu.Lock()
	life := m.lifeCtx
	m.lifecycleMu.Unlock()
	if life == nil {
		return errors.New("runtime has not started")
	}
	select {
	case <-life.Done():
	case <-ctx.Done():
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	return m.terminalErr
}

type runtimeFailure struct {
	role, phase, category string
	cause                 error
	binding               *deploymentconfig.BrokerBinding
}

func (e *runtimeFailure) Error() string { return "runtime task failed: " + e.role }
func (e *runtimeFailure) Unwrap() error { return e.cause }

type initializationCoordinator interface {
	Prepare(context.Context) (initialization.Receipt, error)
	PrepareFresh(context.Context) (initialization.Receipt, error)
	Inspect(context.Context) (initialization.Status, error)
	Finalize(context.Context, string) (initialization.Receipt, error)
}

func (m *Manager) diagnosticAttrs(role, phase, category string) []any {
	fields := []any{"subsystem", "runtime", "role", role, "phase", phase, "category", category}
	if m.cfg != nil && m.cfg.Trigger.Broker != nil {
		binding := m.cfg.Trigger.Broker
		fields = append(fields, "installation_id", binding.Ownership.InstallationID, "instance_id", binding.Ownership.InstanceID, "allocation_id", binding.Ownership.AllocationID, "spec_revision", binding.SpecRevision)
	}
	return fields
}

func SafeError(cfg *config.Config, role, phase, category string, cause error) error {
	err := &runtimeFailure{role: role, phase: phase, category: category, cause: cause}
	if cfg != nil && cfg.Trigger.Broker != nil {
		binding := *cfg.Trigger.Broker
		err.binding = &binding
	}
	return err
}
func (e *runtimeFailure) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("subsystem", "runtime"), slog.String("role", e.role), slog.String("phase", e.phase), slog.String("category", e.category)}
	if e.binding != nil {
		attrs = append(attrs, slog.String("installation_id", e.binding.Ownership.InstallationID), slog.String("instance_id", e.binding.Ownership.InstanceID), slog.String("allocation_id", e.binding.Ownership.AllocationID), slog.Uint64("spec_revision", e.binding.SpecRevision))
	}
	return slog.GroupValue(attrs...)
}
