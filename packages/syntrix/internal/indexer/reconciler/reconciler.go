// Package reconciler provides declarative index state reconciliation.
// It compares desired state (from templates) with actual state (in-memory indexes)
// and executes create/delete/rebuild operations to converge.
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/indexer/manager"
	"github.com/codetreker/syntrix/internal/indexer/rebuild"
	"github.com/codetreker/syntrix/internal/indexer/store"
	"github.com/codetreker/syntrix/internal/indexer/template"
)

// OpType represents the type of reconciliation operation.
type OpType string

const (
	OpCreate  OpType = "create"
	OpDelete  OpType = "delete"
	OpRebuild OpType = "rebuild"
)

// OpStatus represents the status of an operation.
type OpStatus string

const (
	StatusPending OpStatus = "pending"
	StatusRunning OpStatus = "running"
	StatusDone    OpStatus = "done"
	StatusFailed  OpStatus = "failed"
)

// Operation represents a reconciliation operation.
type Operation struct {
	Type       OpType
	Database   string
	Pattern    string
	TemplateID string
	RawPattern string
	Template   *template.Template
	Status     OpStatus
	Progress   int
	Error      string
	StartedAt  time.Time
}

// Config holds reconciler configuration.
type Config struct {
	// Interval between reconciliation loops.
	Interval time.Duration

	// RebuildConfig for rebuild operations.
	RebuildConfig rebuild.Config
}

// DefaultConfig returns the default reconciler configuration.
func DefaultConfig() Config {
	return Config{
		Interval:      5 * time.Second,
		RebuildConfig: rebuild.DefaultConfig(),
	}
}

// Reconciler reconciles desired and actual index state.
type Reconciler struct {
	cfg     Config
	mgr     *manager.Manager
	logger  *slog.Logger
	rebuild *rebuild.Orchestrator

	// Storage scanner and event replayer for rebuild operations
	scanner  rebuild.StorageScanner
	replayer rebuild.EventReplayer

	mu         sync.RWMutex
	running    bool
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	triggerCh  chan struct{}
	pendingOps []Operation
}

// New creates a new Reconciler.
func New(cfg Config, mgr *manager.Manager, orchestrator *rebuild.Orchestrator, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		cfg:       cfg,
		mgr:       mgr,
		rebuild:   orchestrator,
		logger:    logger.With("component", "reconciler"),
		triggerCh: make(chan struct{}, 1),
	}
}

// SetStorageScanner sets the storage scanner and event replayer for rebuild operations.
func (r *Reconciler) SetStorageScanner(scanner rebuild.StorageScanner, replayer rebuild.EventReplayer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanner = scanner
	r.replayer = replayer
}

// Start starts the reconciler loop.
func (r *Reconciler) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return fmt.Errorf("reconciler already running")
	}

	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.running = true
	r.mu.Unlock()

	r.wg.Add(1)
	go r.loop(runCtx)

	r.logger.Info("reconciler started", "interval", r.cfg.Interval)
	return nil
}

// Stop stops the reconciler.
func (r *Reconciler) Stop(ctx context.Context) error {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return nil
	}

	r.cancel()
	r.running = false
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		r.logger.Info("reconciler stopped")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Trigger triggers an immediate reconciliation.
func (r *Reconciler) Trigger() {
	select {
	case r.triggerCh <- struct{}{}:
	default:
		// Already triggered
	}
}

// PendingOperations returns the current pending operations.
func (r *Reconciler) PendingOperations() []Operation {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ops := make([]Operation, len(r.pendingOps))
	copy(ops, r.pendingOps)
	return ops
}

// loop runs the reconciliation loop.
func (r *Reconciler) loop(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	// Initial reconciliation
	r.reconcile(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reconcile(ctx)
		case <-r.triggerCh:
			r.reconcile(ctx)
		}
	}
}

// reconcile performs a single reconciliation cycle.
func (r *Reconciler) reconcile(ctx context.Context) {
	ops := r.computeDiff()

	if len(ops) == 0 {
		return
	}

	r.logger.Info("reconciling", "operations", len(ops))

	r.mu.Lock()
	r.pendingOps = ops
	r.mu.Unlock()

	for i := range ops {
		if ctx.Err() != nil {
			return
		}

		op := &ops[i]
		r.executeOperation(ctx, op)

		// Update pending ops
		r.mu.Lock()
		r.pendingOps = ops
		r.mu.Unlock()
	}

	// Clear completed operations
	r.mu.Lock()
	r.pendingOps = nil
	r.mu.Unlock()
}

// computeDiff computes the operations needed to reconcile desired and actual state.
func (r *Reconciler) computeDiff() []Operation {
	templates := r.mgr.Templates()
	st := r.mgr.Store()
	databases, _ := st.ListDatabases()

	// Build set of desired indexes (pattern|templateID -> template)
	desiredMap := make(map[string]*template.Template)
	for i := range templates {
		t := &templates[i]
		key := t.NormalizedPattern() + "|" + t.Identity()
		desiredMap[key] = t
	}

	// Build set of actual indexes (database|pattern|templateID -> indexInfo)
	actualMap := make(map[string]store.IndexInfo)
	for _, dbName := range databases {
		indexes, _ := st.ListIndexes(dbName)
		for _, idx := range indexes {
			key := dbName + "|" + idx.Pattern + "|" + idx.TemplateID
			actualMap[key] = idx
		}
	}

	var ops []Operation

	// Find indexes to create or rebuild
	for _, dbName := range databases {
		for key, tmpl := range desiredMap {
			fullKey := dbName + "|" + key
			if idx, exists := actualMap[fullKey]; exists {
				// Check if needs rebuild
				if idx.State == store.IndexStateFailed {
					ops = append(ops, Operation{
						Type:       OpRebuild,
						Database:   dbName,
						Pattern:    tmpl.NormalizedPattern(),
						TemplateID: tmpl.Identity(),
						RawPattern: tmpl.CollectionPattern,
						Template:   tmpl,
						Status:     StatusPending,
					})
				}
			} else {
				// Index doesn't exist, create it
				ops = append(ops, Operation{
					Type:       OpCreate,
					Database:   dbName,
					Pattern:    tmpl.NormalizedPattern(),
					TemplateID: tmpl.Identity(),
					RawPattern: tmpl.CollectionPattern,
					Template:   tmpl,
					Status:     StatusPending,
				})
			}
		}
	}

	// Find indexes to delete (actual but not in desired)
	for fullKey, idx := range actualMap {
		// Extract pattern|templateID from fullKey (database|pattern|templateID)
		// We need to check if this pattern|templateID is in desired
		patternKey := idx.Pattern + "|" + idx.TemplateID
		if _, exists := desiredMap[patternKey]; !exists {
			// Parse database from fullKey
			dbName := extractDatabase(fullKey)
			ops = append(ops, Operation{
				Type:       OpDelete,
				Database:   dbName,
				Pattern:    idx.Pattern,
				TemplateID: idx.TemplateID,
				RawPattern: idx.RawPattern,
				Status:     StatusPending,
			})
		}
	}

	return ops
}

// extractDatabase extracts database name from "database|pattern|templateID" key.
func extractDatabase(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == '|' {
			return key[:i]
		}
	}
	return key
}

// executeOperation executes a single reconciliation operation.
func (r *Reconciler) executeOperation(ctx context.Context, op *Operation) {
	op.Status = StatusRunning
	op.StartedAt = time.Now()

	r.logger.Info("executing operation",
		"type", op.Type,
		"database", op.Database,
		"pattern", op.Pattern,
		"templateID", op.TemplateID)

	var err error
	switch op.Type {
	case OpCreate:
		err = r.executeCreate(ctx, op)
	case OpDelete:
		err = r.executeDelete(ctx, op)
	case OpRebuild:
		err = r.executeRebuild(ctx, op)
	}

	if err != nil {
		op.Status = StatusFailed
		op.Error = err.Error()
		r.logger.Error("operation failed",
			"type", op.Type,
			"database", op.Database,
			"pattern", op.Pattern,
			"error", err)
	} else {
		op.Status = StatusDone
		r.logger.Info("operation completed",
			"type", op.Type,
			"database", op.Database,
			"pattern", op.Pattern)
	}
}

// executeCreate creates a new index.
func (r *Reconciler) executeCreate(ctx context.Context, op *Operation) error {
	if op.Template == nil {
		return fmt.Errorf("template not provided")
	}

	st := r.mgr.Store()

	// Mark as rebuilding
	st.SetState(op.Database, op.Pattern, op.TemplateID, store.IndexStateRebuilding)

	// Trigger rebuild if rebuilder is available
	if r.rebuild != nil && r.scanner != nil {
		idxRef := rebuild.IndexRef{
			Database:   op.Database,
			Pattern:    op.Pattern,
			TemplateID: op.TemplateID,
			RawPattern: op.RawPattern,
		}
		_, err := r.rebuild.StartRebuild(ctx, idxRef, op.Template, st, r.scanner, r.replayer)
		return err
	}

	// No rebuilder, just mark as healthy
	st.SetState(op.Database, op.Pattern, op.TemplateID, store.IndexStateHealthy)
	return nil
}

// executeDelete removes an index.
func (r *Reconciler) executeDelete(ctx context.Context, op *Operation) error {
	st := r.mgr.Store()
	return st.DeleteIndex(op.Database, op.Pattern, op.TemplateID)
}

// executeRebuild rebuilds an existing index.
func (r *Reconciler) executeRebuild(ctx context.Context, op *Operation) error {
	st := r.mgr.Store()

	// Trigger rebuild if rebuilder is available
	if r.rebuild != nil && op.Template != nil && r.scanner != nil {
		idxRef := rebuild.IndexRef{
			Database:   op.Database,
			Pattern:    op.Pattern,
			TemplateID: op.TemplateID,
			RawPattern: op.RawPattern,
		}
		_, err := r.rebuild.StartRebuild(ctx, idxRef, op.Template, st, r.scanner, r.replayer)
		return err
	}

	// No rebuilder or template, just clear and mark as healthy
	st.DeleteIndex(op.Database, op.Pattern, op.TemplateID)
	st.SetState(op.Database, op.Pattern, op.TemplateID, store.IndexStateHealthy)
	return nil
}
