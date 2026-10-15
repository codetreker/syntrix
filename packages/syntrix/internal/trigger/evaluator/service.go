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

	// Start blocks until cancellation or a terminal watch/processing failure.
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
	running   bool
	runCtx    context.Context
	terminal  error
	runCancel context.CancelFunc
	startDone chan struct{}

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
func (s *service) Start(ctx context.Context) (result error) {
	s.mu.Lock()
	if s.startDone != nil {
		s.mu.Unlock()
		return errors.New("trigger evaluator is already running or stopped")
	}
	ctx, cancel := context.WithCancel(ctx)
	s.running, s.runCtx, s.terminal = true, ctx, nil
	s.runCancel, s.startDone = cancel, make(chan struct{})
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.running = false
		s.terminal = result
		close(s.startDone)
		s.mu.Unlock()
	}()
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
				if ctx.Err() != nil {
					return nil
				}
				if source, ok := s.watcher.(interface{ TerminalError() error }); ok {
					if err := source.TerminalError(); err != nil {
						return err
					}
				}
				return errors.New("trigger watcher stopped unexpectedly")
			}

			// Guard: skip events with no document data
			if evt.Document == nil && evt.Before == nil {
				slog.Warn("Skipping event with nil Document and Before")
				s.acknowledge(evt.AdmissionSequence)
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
			// Admission cannot pass unfinished publication; checkpoint persistence is asynchronous.
			s.acknowledge(evt.AdmissionSequence)

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

// Shutdown settles the watcher before its borrowed Puller and storage dependencies may close.
func (s *service) Shutdown(ctx context.Context) error {
	s.mu.RLock()
	cancel, started := s.runCancel, s.startDone
	s.mu.RUnlock()
	if started == nil {
		return nil
	}
	cancel()
	select {
	case <-started:
	case <-ctx.Done():
		return ctx.Err()
	}
	if source, ok := s.watcher.(interface{ Shutdown(context.Context) error }); ok {
		return source.Shutdown(ctx)
	}
	return errors.New("trigger watcher settlement is unavailable")
}

func (s *service) acknowledge(sequence uint64) {
	if source, ok := s.watcher.(interface{ Acknowledge(uint64) }); ok {
		source.Acknowledge(sequence)
	}
}

func (s *service) Readiness(ctx context.Context) error {
	s.mu.RLock()
	running, runCtx, terminal := s.running, s.runCtx, s.terminal
	s.mu.RUnlock()
	if terminal != nil {
		return terminal
	}
	if !running {
		return errors.New("trigger evaluator is not running")
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if source, ok := s.watcher.(interface{ Readiness(context.Context) error }); ok {
		return source.Readiness(ctx)
	}
	return errors.New("trigger watcher admission state is unavailable")
}

func (s *service) TerminalError() error {
	s.mu.RLock()
	terminal := s.terminal
	s.mu.RUnlock()
	if terminal != nil {
		return terminal
	}
	if source, ok := s.watcher.(interface{ TerminalError() error }); ok {
		return source.TerminalError()
	}
	return nil
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
