package watcher

import (
	"context"
	"fmt"
	"log"
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
	puller puller.Service
	store  storage.DocumentStore
	opts   WatcherOptions
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
	pullerCh := w.puller.Subscribe(ctx, consumerID, resumeToken)

	outCh := make(chan events.SyntrixChangeEvent)

	go func() {
		defer close(outCh)
		for pEvent := range pullerCh {
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
				continue
			}

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
	return nil
}
