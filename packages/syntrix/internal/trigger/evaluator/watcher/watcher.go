package watcher

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/puller"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/codetreker/syntrix/pkg/model"
)

// WatcherOptions configures the watcher.
type WatcherOptions struct {
	StartFromNow       bool
	CheckpointDatabase string
}

type pullerWatcher struct {
	puller                  puller.Service
	store                   storage.DocumentStore
	opts                    WatcherOptions
	mu                      sync.RWMutex
	admission               *puller.SubscriptionStatus
	terminal                error
	cancel                  context.CancelFunc
	done                    chan struct{}
	delivered, acknowledged uint64
	ackChanged              chan struct{}
	ctx                     context.Context
}

// NewWatcher creates a new DocumentWatcher.
// The watcher receives all events from Puller; database filtering is done by the Evaluator
// based on each trigger's Database field.
func NewWatcher(p puller.Service, store storage.DocumentStore, opts WatcherOptions) DocumentWatcher {
	// Apply default if not set
	if opts.CheckpointDatabase == "" {
		opts.CheckpointDatabase = "default"
	}
	return &pullerWatcher{
		puller: p,
		store:  store,
		opts:   opts,
	}
}

func (w *pullerWatcher) Watch(ctx context.Context) (<-chan events.SyntrixChangeEvent, error) {
	w.mu.Lock()
	if w.done != nil {
		w.mu.Unlock()
		return nil, errors.New("document watcher already started")
	}
	source, ok := w.puller.(puller.AdmissionService)
	if !ok {
		w.mu.Unlock()
		return nil, errors.New("puller subscription admission unavailable")
	}
	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.ctx = ctx
	done := make(chan struct{})
	w.done = done
	w.ackChanged = make(chan struct{})
	w.mu.Unlock()
	handedOff := false
	defer func() {
		if !handedOff {
			cancel()
			close(done)
		}
	}()
	// 1. Load Checkpoint
	// Checkpoint is global (not per-database) because Puller returns a single aggregated
	// progress token across all databases.
	checkpointKey := "sys/checkpoints/trigger_evaluator"
	checkpointDatabase := w.opts.CheckpointDatabase

	var resumeToken string
	checkpointDoc, err := w.store.Get(ctx, checkpointDatabase, checkpointKey)
	if err == nil && checkpointDoc != nil {
		if token, ok := checkpointDoc.Data["token"].(string); ok {
			resumeToken = token
			log.Printf("Resuming trigger watcher from checkpoint: %s", token)
		}
	} else if err == model.ErrNotFound {
		if !w.opts.StartFromNow {
			return nil, fmt.Errorf("checkpoint not found and StartFromNow is false")
		}
		log.Printf("AUDIT: Starting watch from NOW (checkpoint missing)")
	} else {
		log.Printf("Failed to load checkpoint: %v", err)
		return nil, err
	}

	// 2. Start Watch
	consumerID := "trigger-evaluator"
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pullerCh, admission := source.SubscribeStatus(ctx, consumerID, resumeToken)
	if pullerCh == nil || admission == nil {
		return nil, errors.New("puller returned no admission subscription")
	}
	w.mu.Lock()
	w.admission = admission
	w.mu.Unlock()

	outCh := make(chan events.SyntrixChangeEvent)

	handedOff = true
	go func() {
		defer close(done)
		defer close(outCh)
		for {
			var pEvent *events.PullerEvent
			select {
			case <-ctx.Done():
				return
			case evt, open := <-pullerCh:
				if !open {
					if ctx.Err() == nil {
						cause := admission.Snapshot().Err
						if cause == nil {
							cause = errors.New("puller watcher closed unexpectedly")
						}
						w.fail(cause)
					}
					return
				}
				pEvent = evt
			}
			if pEvent == nil {
				w.fail(errors.New("puller returned nil event"))
				return
			}
			if pEvent.Error != nil {
				w.fail(pEvent.Error)
				return
			}
			if pEvent.AdmissionState != "" {
				if pEvent.Ready {
					if err := w.waitAcknowledged(ctx); err != nil {
						return
					}
				}
				admission.Observe(pEvent)
				continue
			}
			if pEvent.Change == nil {
				continue
			}
			// Our checkpoint writes reenter Puller; evaluating them would keep
			// advancing the checkpoint without a document change to process.
			if pEvent.Change != nil && pEvent.Change.FullDocument != nil &&
				pEvent.Change.FullDocument.Database == checkpointDatabase &&
				pEvent.Change.FullDocument.Fullpath == checkpointKey {
				continue
			}
			// Convert puller event to SyntrixChangeEvent
			// No database filtering here - Evaluator handles database matching per trigger
			event, err := events.Transform(pEvent)
			if err != nil {
				if errors.Is(err, events.ErrDeleteOPIgnored) {
					continue
				}
				w.fail(err)
				return
			}
			w.mu.Lock()
			w.delivered++
			event.AdmissionSequence = w.delivered
			w.mu.Unlock()

			select {
			case outCh <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	return outCh, nil
}

func (w *pullerWatcher) SaveCheckpoint(ctx context.Context, token interface{}) error {
	checkpointKey := "sys/checkpoints/trigger_evaluator"
	checkpointDatabase := w.opts.CheckpointDatabase

	data := map[string]interface{}{
		"token":     token,
		"updatedAt": time.Now().Unix(),
	}

	err := w.store.Update(ctx, checkpointDatabase, checkpointKey, data, model.Filters{})
	if err != nil {
		if err == model.ErrNotFound {
			// Create if not exists
			doc := storage.NewStoredDoc(checkpointDatabase, "sys/checkpoints", "trigger_evaluator", data)
			return w.store.Create(ctx, checkpointDatabase, doc)
		}
		return err
	}
	return nil
}

// Close releases resources held by the watcher.
func (w *pullerWatcher) Close() error {
	return w.Shutdown(context.Background())
}

func (w *pullerWatcher) Shutdown(ctx context.Context) error {
	w.mu.RLock()
	cancel, done := w.cancel, w.done
	w.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
			return nil
		default:
		}
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (w *pullerWatcher) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.terminal == nil {
		w.terminal = err
	}
	if w.admission != nil {
		w.admission.Fail(err, true)
	}
	if w.cancel != nil {
		w.cancel()
	}
}

func (w *pullerWatcher) Readiness(ctx context.Context) error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.terminal != nil {
		return w.terminal
	}
	if w.admission == nil {
		return events.ErrAdmissionUnavailable
	}
	if err := w.ctx.Err(); err != nil {
		return err
	}
	return w.admission.Err(ctx)
}

func (w *pullerWatcher) TerminalError() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.terminal != nil {
		return w.terminal
	}
	if w.admission != nil {
		snap := w.admission.Snapshot()
		if snap.Terminal && !errors.Is(snap.Err, context.Canceled) {
			return snap.Err
		}
	}
	return nil
}

func (w *pullerWatcher) Acknowledge(sequence uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sequence > w.acknowledged && sequence <= w.delivered {
		w.acknowledged = sequence
		close(w.ackChanged)
		w.ackChanged = make(chan struct{})
	}
}

func (w *pullerWatcher) waitAcknowledged(ctx context.Context) error {
	for {
		w.mu.RLock()
		ready, changed := w.acknowledged >= w.delivered, w.ackChanged
		w.mu.RUnlock()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
