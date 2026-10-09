package evaluator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/codetreker/syntrix/internal/trigger/evaluator/watcher"
	"github.com/codetreker/syntrix/internal/trigger/types"
)

const (
	initialRetryDelay = time.Second
	maxRetryDelay     = 30 * time.Second
)

// Service evaluates document changes against trigger rules and publishes matched tasks.
type Service interface {
	// LoadTriggers validates and loads trigger rules.
	LoadTriggers(triggers []*types.Trigger) error

	// Start begins watching for changes and evaluating triggers.
	// Blocks until context is cancelled.
	Start(ctx context.Context) error

	// Close releases resources.
	Close() error
}

// service implements the Service interface.
type service struct {
	evaluator Evaluator
	watcher   watcher.DocumentWatcher
	publisher TaskPublisher
	triggers  []*types.Trigger
	mu        sync.RWMutex

	// Async checkpoint saving
	latestProgress   string
	progressMu       sync.Mutex
	checkpointNotify chan struct{}
	checkpointDone   chan struct{}
}

// LoadTriggers validates and loads the given triggers.
func (s *service) LoadTriggers(triggers []*types.Trigger) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, t := range triggers {
		if err := ValidateTrigger(t); err != nil {
			return err
		}
	}

	s.triggers = triggers
	return nil
}

// Start begins the trigger processing loop.
func (s *service) Start(ctx context.Context) error {
	stream, err := s.watcher.Watch(ctx)
	if err != nil {
		return err
	}

	// Initialize async checkpoint saving
	s.checkpointNotify = make(chan struct{}, 1)
	s.checkpointDone = make(chan struct{})
	checkpointCtx, stopCheckpoint := context.WithCancel(ctx)
	defer func() {
		stopCheckpoint()
		<-s.checkpointDone
	}()

	// Start checkpoint saver goroutine
	go s.checkpointSaver(checkpointCtx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case evt, ok := <-stream:
			if !ok {
				return nil
			}

			// Guard: skip events with no document data
			if evt.Document == nil && evt.Before == nil {
				slog.Warn("Skipping event with nil Document and Before")
				continue
			}
			if evt.Progress == "" {
				return fmt.Errorf("trigger event %q has no puller progress", evt.Id)
			}

			if err := s.processEvent(ctx, evt); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}

			// Update latest progress and notify (non-blocking)
			if evt.Progress != "" {
				s.progressMu.Lock()
				s.latestProgress = evt.Progress
				s.progressMu.Unlock()

				// Non-blocking send to notify
				select {
				case s.checkpointNotify <- struct{}{}:
				default:
					// Already notified, skip
				}
			}
		}
	}
}

func (s *service) processEvent(ctx context.Context, evt events.SyntrixChangeEvent) error {
	s.mu.RLock()
	currentTriggers := s.triggers
	s.mu.RUnlock()

	for _, t := range currentTriggers {
		var matched bool
		if err := retryUntilSuccess(ctx, t.ID, evt.Id, "evaluate", func() error {
			var err error
			matched, err = s.evaluator.Evaluate(ctx, t, evt)
			return err
		}); err != nil {
			return err
		}
		if !matched {
			continue
		}
		if s.publisher == nil {
			return errors.New("trigger task publisher is not configured")
		}

		task := deliveryTask(t, evt)
		if err := retryUntilSuccess(ctx, t.ID, evt.Id, "publish", func() error {
			return s.publisher.Publish(ctx, task)
		}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func deliveryTask(t *types.Trigger, evt events.SyntrixChangeEvent) *types.DeliveryTask {
	doc := evt.Document
	if doc == nil {
		doc = evt.Before
	}
	timeout := t.Timeout
	if timeout == 0 {
		timeout = types.Duration(types.DefaultTaskTimeout)
	}
	return &types.DeliveryTask{
		TriggerID:   t.ID,
		Database:    t.Database,
		Event:       string(evt.Type),
		Collection:  doc.Collection,
		DocumentID:  doc.Id,
		Payload:     doc.Data,
		URL:         t.URL,
		Headers:     t.Headers,
		SecretsRef:  t.SecretsRef,
		RetryPolicy: t.RetryPolicy,
		Timeout:     timeout,
	}
}

func retryUntilSuccess(ctx context.Context, triggerID, eventID, operation string, attempt func() error) error {
	delay := initialRetryDelay
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := attempt(); err == nil {
			return nil
		} else {
			slog.Error("Trigger event processing failed; retrying", "trigger_id", triggerID, "event_id", eventID, "operation", operation, "retry_delay", delay, "error", err)
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < maxRetryDelay/2 {
			delay *= 2
		} else {
			delay = maxRetryDelay
		}
	}
}

// checkpointSaver runs in a goroutine and saves checkpoints asynchronously.
func (s *service) checkpointSaver(ctx context.Context) {
	defer close(s.checkpointDone)

	for {
		select {
		case <-ctx.Done():
			// Save final checkpoint before exit
			s.saveLatestCheckpoint(ctx)
			return
		case <-s.checkpointNotify:
			s.saveLatestCheckpoint(ctx)
		}
	}
}

// saveLatestCheckpoint saves the current latest progress.
func (s *service) saveLatestCheckpoint(ctx context.Context) {
	s.progressMu.Lock()
	progress := s.latestProgress
	s.progressMu.Unlock()

	if progress == "" {
		return
	}

	if err := s.watcher.SaveCheckpoint(ctx, progress); err != nil {
		slog.Error("Failed to save checkpoint", "error", err)
	}
}

// Close stops the service and releases resources.
func (s *service) Close() error {
	var errs []error

	if s.watcher != nil {
		if err := s.watcher.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if s.publisher != nil {
		if err := s.publisher.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errs[0] // Return first error
	}
	return nil
}
