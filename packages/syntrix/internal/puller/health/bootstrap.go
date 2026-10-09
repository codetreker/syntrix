package health

import (
	"context"
	"log/slog"
	"sync"

	"go.mongodb.org/mongo-driver/bson"
)

// CheckpointLoader defines the interface for loading checkpoints.
type CheckpointLoader interface {
	LoadCheckpoint() (bson.Raw, error)
}

// BootstrapMode defines how to start the change stream.
type BootstrapMode string

const (
	// BootstrapFromNow starts from the current position (no historical events).
	BootstrapFromNow BootstrapMode = "from_now"

	// BootstrapFromBeginning starts from the beginning of the oplog.
	// Warning: This may take a long time for large databases.
	BootstrapFromBeginning BootstrapMode = "from_beginning"
)

// Bootstrap handles first-run initialization and mode detection.
type Bootstrap struct {
	mode       BootstrapMode
	checkpoint CheckpointLoader
	logger     *slog.Logger

	// mu protects state
	mu sync.RWMutex

	// bootstrapped indicates if bootstrap has completed
	bootstrapped bool

	// isFirstRun indicates if this is the first run (no checkpoint)
	isFirstRun bool
}

// BootstrapOptions configures the bootstrap.
type BootstrapOptions struct {
	Mode       BootstrapMode
	Checkpoint CheckpointLoader
	Logger     *slog.Logger
}

// NewBootstrap creates a new bootstrap handler.
func NewBootstrap(opts BootstrapOptions) *Bootstrap {
	mode := opts.Mode
	if mode == "" {
		mode = BootstrapFromNow
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Bootstrap{
		mode:       mode,
		checkpoint: opts.Checkpoint,
		logger:     logger.With("component", "bootstrap"),
	}
}

// Run performs bootstrap initialization.
// Returns true if this is the first run.
func (b *Bootstrap) Run(ctx context.Context) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.bootstrapped {
		return b.isFirstRun, nil
	}

	// Check if we have a checkpoint
	if b.checkpoint != nil {
		token, err := b.checkpoint.LoadCheckpoint()
		if err != nil {
			return false, err
		}

		b.isFirstRun = token == nil
	} else {
		b.isFirstRun = true
	}

	if b.isFirstRun {
		b.logger.Info("first run detected",
			"mode", b.mode,
		)
	} else {
		b.logger.Info("resuming from checkpoint")
	}

	b.bootstrapped = true
	return b.isFirstRun, nil
}

// IsFirstRun returns true if this is the first run.
func (b *Bootstrap) IsFirstRun() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.isFirstRun
}

// Mode returns the bootstrap mode.
func (b *Bootstrap) Mode() BootstrapMode {
	return b.mode
}

// IsBootstrapped returns true if bootstrap has completed.
func (b *Bootstrap) IsBootstrapped() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.bootstrapped
}
