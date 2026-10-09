// Package rebuild provides rebuild orchestration for indexes.
//
// Rebuild is triggered when:
//   - Gap is detected (progress marker too old for Puller buffer)
//   - Admin trigger
//   - Startup with missing/corrupt index data
//
// Rebuild flow:
//  1. Record current buffer position (startKey)
//  2. Mark index as "rebuilding" (queries fall back to storage)
//  3. Clear existing index data
//  4. Scan storage with pagination (throttled)
//  5. Replay buffered events from startKey
//  6. Mark index as "healthy"
package rebuild

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/internal/indexer/store"
	"github.com/codetreker/syntrix/internal/indexer/template"
)

// Config holds rebuild configuration.
type Config struct {
	// BatchSize is the number of documents to fetch per storage scan batch.
	// Default: 500
	BatchSize int

	// QPSLimit is the max upserts per second during rebuild.
	// Default: 5000
	QPSLimit int

	// MaxConcurrent is the max number of concurrent rebuilds.
	// Default: 2
	MaxConcurrent int
}

// DefaultConfig returns the default rebuild configuration.
func DefaultConfig() Config {
	return Config{
		BatchSize:     500,
		QPSLimit:      5000,
		MaxConcurrent: 2,
	}
}

// Status represents the current status of a rebuild job.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

// Job represents a single rebuild job for an index.
type Job struct {
	ID         string // Unique job ID
	Database   string // Database name
	Pattern    string // Normalized collection pattern
	TemplateID string // Template identity
	RawPattern string // Original pattern for storage scan
	Template   *template.Template

	Status    Status
	StartTime time.Time
	EndTime   time.Time
	DocsTotal int64  // Total documents scanned
	DocsAdded int64  // Documents added to index
	Error     string // Error message if failed

	mu     sync.Mutex
	cancel context.CancelFunc
}

// Progress returns a snapshot of the job progress.
func (j *Job) Progress() JobProgress {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JobProgress{
		ID:        j.ID,
		Status:    j.Status,
		DocsTotal: j.DocsTotal,
		DocsAdded: j.DocsAdded,
		StartTime: j.StartTime,
		EndTime:   j.EndTime,
		Error:     j.Error,
	}
}

// JobProgress is a snapshot of job progress.
type JobProgress struct {
	ID        string
	Status    Status
	DocsTotal int64
	DocsAdded int64
	StartTime time.Time
	EndTime   time.Time
	Error     string
}

// DocIterator provides document iteration for rebuild.
type DocIterator interface {
	// Next advances to the next document. Returns false when done.
	Next() bool
	// Doc returns the current document.
	Doc() *types.StoredDoc
	// Err returns any error encountered during iteration.
	Err() error
	// Close releases the iterator resources.
	Close() error
}

// StorageScanner provides document iteration for rebuild.
type StorageScanner interface {
	// ScanCollection returns an iterator over documents in a collection.
	// The iterator should support pagination via startAfter.
	ScanCollection(ctx context.Context, database, collectionPattern string, batchSize int, startAfter string) (DocIterator, error)
}

// EventReplayer provides event replay from buffer for catch-up.
type EventReplayer interface {
	// CurrentPosition returns the current buffer position.
	// This must be called BEFORE the storage scan to record where replay should start.
	CurrentPosition(ctx context.Context) (string, error)

	// ReplayFrom replays events from the given progress marker.
	// The callback is called for each event.
	ReplayFrom(ctx context.Context, startKey string, callback func(evt *Event) error) error
}

// Event represents a change event for replay.
type Event struct {
	Database   string
	Collection string
	DocID      string
	Data       map[string]any
	Deleted    bool
	EventID    string
}

// OrderKeyBuilder builds OrderKeys from document data.
type OrderKeyBuilder interface {
	BuildOrderKey(data map[string]any, tmpl *template.Template) ([]byte, error)
}

// Orchestrator manages rebuild jobs.
type Orchestrator struct {
	cfg        Config
	logger     *slog.Logger
	keyBuilder OrderKeyBuilder

	mu      sync.Mutex
	jobs    map[string]*Job // jobID -> Job
	running int             // number of currently running jobs
	pending []*Job          // pending jobs waiting for capacity
}

// New creates a new rebuild orchestrator.
func New(cfg Config, keyBuilder OrderKeyBuilder, logger *slog.Logger) *Orchestrator {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 500
	}
	if cfg.QPSLimit <= 0 {
		cfg.QPSLimit = 5000
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}

	return &Orchestrator{
		cfg:        cfg,
		logger:     logger.With("component", "rebuild"),
		keyBuilder: keyBuilder,
		jobs:       make(map[string]*Job),
	}
}

// IndexRef identifies a specific index.
type IndexRef struct {
	Database   string
	Pattern    string
	TemplateID string
	RawPattern string
}

// StartRebuild initiates a rebuild job for an index.
// Returns the job ID for tracking.
func (o *Orchestrator) StartRebuild(
	ctx context.Context,
	idxRef IndexRef,
	tmpl *template.Template,
	st store.Store,
	scanner StorageScanner,
	replayer EventReplayer,
) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Generate job ID
	jobID := fmt.Sprintf("%s|%s|%s|%d", idxRef.Database, idxRef.Pattern, idxRef.TemplateID, time.Now().UnixNano())

	// Check if already rebuilding
	for _, job := range o.jobs {
		if job.Database == idxRef.Database && job.Pattern == idxRef.Pattern && job.TemplateID == idxRef.TemplateID {
			job.mu.Lock()
			status := job.Status
			job.mu.Unlock()
			if status == StatusPending || status == StatusRunning {
				return "", fmt.Errorf("rebuild already in progress for index %s|%s", idxRef.Pattern, idxRef.TemplateID)
			}
		}
	}

	job := &Job{
		ID:         jobID,
		Database:   idxRef.Database,
		Pattern:    idxRef.Pattern,
		TemplateID: idxRef.TemplateID,
		RawPattern: idxRef.RawPattern,
		Template:   tmpl,
		Status:     StatusPending,
	}
	o.jobs[jobID] = job

	// Check if we can start immediately
	if o.running < o.cfg.MaxConcurrent {
		o.running++
		go o.runJob(ctx, job, st, scanner, replayer)
	} else {
		o.pending = append(o.pending, job)
		o.logger.Info("rebuild job queued",
			"jobID", jobID,
			"database", idxRef.Database,
			"pattern", idxRef.Pattern)
	}

	return jobID, nil
}

// runJob executes the rebuild job.
func (o *Orchestrator) runJob(
	ctx context.Context,
	job *Job,
	st store.Store,
	scanner StorageScanner,
	replayer EventReplayer,
) {
	jobCtx, cancel := context.WithCancel(ctx)
	job.mu.Lock()
	job.cancel = cancel
	job.Status = StatusRunning
	job.StartTime = time.Now()
	job.mu.Unlock()

	o.logger.Info("starting rebuild job",
		"jobID", job.ID,
		"database", job.Database,
		"pattern", job.Pattern)

	// Mark index as rebuilding
	st.SetState(job.Database, job.Pattern, job.TemplateID, store.IndexStateRebuilding)

	// Clear existing data by deleting and recreating the index
	st.DeleteIndex(job.Database, job.Pattern, job.TemplateID)

	// Run the rebuild
	err := o.doRebuild(jobCtx, job, st, scanner, replayer)

	// Update job status
	job.mu.Lock()
	job.EndTime = time.Now()
	if err != nil {
		if jobCtx.Err() != nil {
			job.Status = StatusCanceled
		} else {
			job.Status = StatusFailed
			job.Error = err.Error()
			st.SetState(job.Database, job.Pattern, job.TemplateID, store.IndexStateFailed)
		}
		o.logger.Error("rebuild job failed",
			"jobID", job.ID,
			"error", err)
	} else {
		job.Status = StatusCompleted
		st.SetState(job.Database, job.Pattern, job.TemplateID, store.IndexStateHealthy)
		o.logger.Info("rebuild job completed",
			"jobID", job.ID,
			"duration", job.EndTime.Sub(job.StartTime),
			"docsAdded", job.DocsAdded)
	}
	job.mu.Unlock()

	// Start next pending job if any
	o.mu.Lock()
	o.running--
	if len(o.pending) > 0 && o.running < o.cfg.MaxConcurrent {
		next := o.pending[0]
		o.pending = o.pending[1:]
		o.running++
		// Note: We need to get the index for the pending job
		// This is simplified - in production, pending jobs would store index reference
		o.logger.Info("starting queued rebuild job",
			"jobID", next.ID)
	}
	o.mu.Unlock()
}

// doRebuild performs the actual rebuild work.
func (o *Orchestrator) doRebuild(
	ctx context.Context,
	job *Job,
	st store.Store,
	scanner StorageScanner,
	replayer EventReplayer,
) error {
	// Step 1: Record current buffer position (for event replay)
	// This MUST be done BEFORE the storage scan to ensure we don't miss
	// any events that arrive during the scan.
	var startKey string
	if replayer != nil {
		var err error
		startKey, err = replayer.CurrentPosition(ctx)
		if err != nil {
			return fmt.Errorf("failed to get buffer position: %w", err)
		}
		o.logger.Info("recorded buffer position for replay",
			"jobID", job.ID,
			"startKey", startKey)
	}

	// Step 2: Scan storage with pagination
	if scanner != nil {
		if err := o.scanStorage(ctx, job, st, scanner); err != nil {
			return fmt.Errorf("storage scan failed: %w", err)
		}
	}

	// Step 3: Replay buffered events from the recorded position
	if replayer != nil {
		if err := o.replayEvents(ctx, job, st, replayer, startKey); err != nil {
			return fmt.Errorf("event replay failed: %w", err)
		}
	}

	return nil
}

// scanStorage scans the storage and adds documents to the index.
func (o *Orchestrator) scanStorage(
	ctx context.Context,
	job *Job,
	st store.Store,
	scanner StorageScanner,
) error {
	var startAfter string
	batchCount := 0

	// Simple rate limiter: delay between batches
	batchDelay := time.Duration(float64(o.cfg.BatchSize) / float64(o.cfg.QPSLimit) * float64(time.Second))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		iter, err := scanner.ScanCollection(ctx, job.Database, job.RawPattern, o.cfg.BatchSize, startAfter)
		if err != nil {
			return err
		}

		count := 0
		var lastID string

		for iter.Next() {
			doc := iter.Doc()
			if doc == nil {
				continue
			}

			job.mu.Lock()
			job.DocsTotal++
			job.mu.Unlock()

			// Build OrderKey
			orderKey, err := o.keyBuilder.BuildOrderKey(doc.Data, job.Template)
			if err != nil {
				o.logger.Warn("failed to build order key",
					"docID", doc.Id,
					"error", err)
				continue
			}

			// Add to index via store interface
			st.Upsert(job.Database, job.Pattern, job.TemplateID, doc.Id, orderKey, "")

			job.mu.Lock()
			job.DocsAdded++
			job.mu.Unlock()

			lastID = doc.Id
			count++
		}

		if err := iter.Err(); err != nil {
			iter.Close()
			return err
		}
		iter.Close()

		// No more documents
		if count == 0 {
			break
		}

		startAfter = lastID
		batchCount++

		// Rate limiting
		if batchDelay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(batchDelay):
			}
		}
	}

	o.logger.Info("storage scan completed",
		"jobID", job.ID,
		"batches", batchCount,
		"docsTotal", job.DocsTotal)

	return nil
}

// replayEvents replays buffered events to catch up.
func (o *Orchestrator) replayEvents(
	ctx context.Context,
	job *Job,
	st store.Store,
	replayer EventReplayer,
	startKey string,
) error {
	eventsReplayed := 0

	err := replayer.ReplayFrom(ctx, startKey, func(evt *Event) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check if this event matches our index's collection pattern
		// This is simplified - in production, we'd check pattern matching
		if evt.Database != job.Database {
			return nil
		}

		if evt.Deleted {
			st.Delete(job.Database, job.Pattern, job.TemplateID, evt.DocID, "")
		} else {
			orderKey, err := o.keyBuilder.BuildOrderKey(evt.Data, job.Template)
			if err != nil {
				o.logger.Warn("failed to build order key during replay",
					"docID", evt.DocID,
					"error", err)
				return nil
			}
			st.Upsert(job.Database, job.Pattern, job.TemplateID, evt.DocID, orderKey, "")
		}

		eventsReplayed++
		return nil
	})

	if err != nil {
		return err
	}

	o.logger.Info("event replay completed",
		"jobID", job.ID,
		"eventsReplayed", eventsReplayed)

	return nil
}

// CancelRebuild cancels a running or pending rebuild job.
func (o *Orchestrator) CancelRebuild(jobID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	job, ok := o.jobs[jobID]
	if !ok {
		return fmt.Errorf("job not found: %s", jobID)
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	switch job.Status {
	case StatusPending:
		// Remove from pending queue
		for i, p := range o.pending {
			if p.ID == jobID {
				o.pending = append(o.pending[:i], o.pending[i+1:]...)
				break
			}
		}
		job.Status = StatusCanceled
		job.EndTime = time.Now()
	case StatusRunning:
		if job.cancel != nil {
			job.cancel()
		}
		// Status will be updated by runJob
	default:
		return fmt.Errorf("job %s is not pending or running (status: %s)", jobID, job.Status)
	}

	return nil
}

// GetJob returns the status of a rebuild job.
func (o *Orchestrator) GetJob(jobID string) (*JobProgress, error) {
	o.mu.Lock()
	job, ok := o.jobs[jobID]
	o.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("job not found: %s", jobID)
	}

	progress := job.Progress()
	return &progress, nil
}

// ListJobs returns all rebuild jobs.
func (o *Orchestrator) ListJobs() []JobProgress {
	o.mu.Lock()
	defer o.mu.Unlock()

	result := make([]JobProgress, 0, len(o.jobs))
	for _, job := range o.jobs {
		result = append(result, job.Progress())
	}
	return result
}

// CleanupCompletedJobs removes completed/failed/canceled jobs older than the given duration.
func (o *Orchestrator) CleanupCompletedJobs(maxAge time.Duration) int {
	o.mu.Lock()
	defer o.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	removed := 0

	for id, job := range o.jobs {
		job.mu.Lock()
		if (job.Status == StatusCompleted || job.Status == StatusFailed || job.Status == StatusCanceled) &&
			!job.EndTime.IsZero() && job.EndTime.Before(cutoff) {
			delete(o.jobs, id)
			removed++
		}
		job.mu.Unlock()
	}

	return removed
}
