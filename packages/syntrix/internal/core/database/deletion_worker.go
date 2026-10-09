package database

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage/types"
)

// IndexInvalidator is an interface for invalidating database indexes.
// This interface is implemented by indexer.LocalService.
type IndexInvalidator interface {
	InvalidateDatabase(ctx context.Context, database string) error
}

// DeletionWorkerConfig contains configuration for the deletion worker
type DeletionWorkerConfig struct {
	// Interval between cleanup cycles
	Interval time.Duration
	// BatchSize is the number of documents to delete per batch
	BatchSize int
	// MaxBatchesPerCycle limits the number of batches per database per cycle
	MaxBatchesPerCycle int
}

// DefaultDeletionWorkerConfig returns the default configuration
func DefaultDeletionWorkerConfig() DeletionWorkerConfig {
	return DeletionWorkerConfig{
		Interval:           5 * time.Minute,
		BatchSize:          100,
		MaxBatchesPerCycle: 10,
	}
}

// DeletionWorker handles background cleanup of deleted databases
type DeletionWorker struct {
	dbStore     DatabaseStore
	docStore    types.DocumentStore
	indexer     IndexInvalidator
	config      DeletionWorkerConfig
	logger      *slog.Logger
	mu          sync.RWMutex
	running     bool
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	lastRunTime time.Time
}

// NewDeletionWorker creates a new deletion worker
func NewDeletionWorker(
	dbStore DatabaseStore,
	docStore types.DocumentStore,
	indexerSvc IndexInvalidator,
	config DeletionWorkerConfig,
	logger *slog.Logger,
) *DeletionWorker {
	if config.Interval <= 0 {
		config = DefaultDeletionWorkerConfig()
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 100
	}
	if config.MaxBatchesPerCycle <= 0 {
		config.MaxBatchesPerCycle = 10
	}

	return &DeletionWorker{
		dbStore:  dbStore,
		docStore: docStore,
		indexer:  indexerSvc,
		config:   config,
		logger:   logger.With("component", "deletion-worker"),
	}
}

// Start starts the deletion worker
func (w *DeletionWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil
	}

	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.running = true
	w.mu.Unlock()

	w.wg.Add(1)
	go w.runLoop(workerCtx)

	w.logger.Info("deletion worker started",
		"interval", w.config.Interval,
		"batchSize", w.config.BatchSize)
	return nil
}

// Stop stops the deletion worker
func (w *DeletionWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return nil
	}

	if w.cancel != nil {
		w.cancel()
	}
	w.running = false
	w.mu.Unlock()

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		w.logger.Info("deletion worker stopped")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runLoop runs the deletion worker loop
func (w *DeletionWorker) runLoop(ctx context.Context) {
	defer w.wg.Done()

	// Run immediately on start
	w.processDeletingDatabases(ctx)

	ticker := time.NewTicker(w.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processDeletingDatabases(ctx)
		}
	}
}

// processDeletingDatabases finds databases with status=deleting and cleans them up
func (w *DeletionWorker) processDeletingDatabases(ctx context.Context) {
	w.mu.Lock()
	w.lastRunTime = time.Now()
	w.mu.Unlock()

	// List databases with status=deleting
	databases, _, err := w.dbStore.List(ctx, ListOptions{
		Status: StatusDeleting,
		Limit:  100, // Process up to 100 databases per cycle
	})
	if err != nil {
		w.logger.Error("failed to list deleting databases", "error", err)
		return
	}

	if len(databases) == 0 {
		return
	}

	w.logger.Info("processing deleting databases", "count", len(databases))

	for _, db := range databases {
		if ctx.Err() != nil {
			return
		}
		w.processDatabase(ctx, db)
	}
}

// processDatabase handles the deletion of a single database
func (w *DeletionWorker) processDatabase(ctx context.Context, db *Database) {
	logger := w.logger.With("database_id", db.ID)
	if db.Slug != nil {
		logger = logger.With("database_slug", *db.Slug)
	}

	logger.Info("processing database for deletion")

	// Phase 1: Delete documents in batches
	totalDeleted := 0
	for batch := 0; batch < w.config.MaxBatchesPerCycle; batch++ {
		if ctx.Err() != nil {
			return
		}

		deleted, err := w.docStore.DeleteByDatabase(ctx, db.ID, w.config.BatchSize)
		if err != nil {
			logger.Error("failed to delete documents", "error", err, "batch", batch)
			return // Stop processing this database, will retry next cycle
		}

		totalDeleted += deleted

		if deleted < w.config.BatchSize {
			// No more documents to delete
			break
		}
	}

	if totalDeleted > 0 {
		logger.Info("deleted documents", "count", totalDeleted)
	}

	// Check if there are more documents remaining
	remaining, err := w.docStore.DeleteByDatabase(ctx, db.ID, 1)
	if err != nil {
		logger.Error("failed to check remaining documents", "error", err)
		return
	}

	if remaining > 0 {
		logger.Info("more documents remain, will continue next cycle")
		return // More work to do, don't finalize yet
	}

	// Phase 2: Invalidate indexes
	if w.indexer != nil {
		if err := w.indexer.InvalidateDatabase(ctx, db.ID); err != nil {
			logger.Error("failed to invalidate indexes", "error", err)
			// Continue with hard delete anyway - indexes will be orphaned but harmless
		}
	}

	// Phase 3: Hard delete the database metadata
	if err := w.dbStore.Delete(ctx, db.ID); err != nil {
		logger.Error("failed to hard delete database", "error", err)
		return
	}

	logger.Info("database deletion completed")
}

// LastRunTime returns the time of the last cleanup run
func (w *DeletionWorker) LastRunTime() time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastRunTime
}

// IsRunning returns whether the worker is running
func (w *DeletionWorker) IsRunning() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.running
}
