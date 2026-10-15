package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/codetreker/syntrix/internal/trigger/delivery/worker"
	"github.com/codetreker/syntrix/internal/trigger/types"
)

// TaskConsumer consumes delivery tasks.
type TaskConsumer interface {
	Start(ctx context.Context) error
}

// DefaultChannelBufferSize is the default buffer size for worker channels.
const DefaultChannelBufferSize = 100

// natsConsumer consumes delivery tasks from NATS and dispatches them to the worker.
type natsConsumer struct {
	consumer       pubsub.Consumer
	worker         worker.DeliveryWorker
	numWorkers     int
	channelBufSize int
	workerChans    []chan pubsub.Message
	wg             sync.WaitGroup
	metrics        types.Metrics

	// Shutdown coordination
	closing         atomic.Bool  // Marks closing state
	inFlightCount   atomic.Int32 // Count of messages currently in dispatch()
	drainTimeout    time.Duration
	shutdownTimeout time.Duration
	activeWorkers   atomic.Int32
	stateMu         sync.RWMutex
	running         bool
	runCtx          context.Context
	terminal        error
	runCancel       context.CancelFunc
	startDone       chan struct{}
	workersDone     chan struct{}
	messages        <-chan pubsub.Message
	settlementGate  chan struct{}
}

// ConsumerOption configures the consumer.
type ConsumerOption func(*natsConsumer)

// WithChannelBufferSize sets the buffer size for worker channels.
func WithChannelBufferSize(size int) ConsumerOption {
	return func(c *natsConsumer) {
		if size > 0 {
			c.channelBufSize = size
		}
	}
}

// WithDrainTimeout sets the drain timeout for graceful shutdown.
// This is the maximum time to wait for in-flight dispatch() calls to complete.
func WithDrainTimeout(d time.Duration) ConsumerOption {
	return func(c *natsConsumer) {
		if d > 0 {
			c.drainTimeout = d
		}
	}
}

// WithShutdownTimeout sets the overall shutdown timeout.
// This is the maximum time to wait for workers to finish processing.
func WithShutdownTimeout(d time.Duration) ConsumerOption {
	return func(c *natsConsumer) {
		if d > 0 {
			c.shutdownTimeout = d
		}
	}
}

// NewTaskConsumer creates a new TaskConsumer wrapping a pubsub.Consumer.
func NewTaskConsumer(consumer pubsub.Consumer, w worker.DeliveryWorker, numWorkers int, metrics types.Metrics, opts ...ConsumerOption) TaskConsumer {
	if numWorkers <= 0 {
		numWorkers = 16
	}
	if metrics == nil {
		metrics = &types.NoopMetrics{}
	}

	c := &natsConsumer{
		consumer:        consumer,
		worker:          w,
		numWorkers:      numWorkers,
		channelBufSize:  DefaultChannelBufferSize,
		metrics:         metrics,
		drainTimeout:    types.DefaultDrainTimeout,
		shutdownTimeout: types.DefaultShutdownTimeout,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Start blocks until cancellation or terminal consumer failure and settles the source and worker pool.
func (c *natsConsumer) Start(ctx context.Context) (result error) {
	c.stateMu.Lock()
	if c.startDone != nil {
		c.stateMu.Unlock()
		return errors.New("trigger delivery pool is already running or stopped")
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.running, c.runCtx, c.terminal = true, runCtx, nil
	c.runCancel, c.startDone = cancel, make(chan struct{})
	c.settlementGate = make(chan struct{}, 1)
	c.closing.Store(false)
	c.stateMu.Unlock()
	defer func() {
		cancel()
		c.stateMu.Lock()
		c.running, c.terminal = false, result
		close(c.startDone)
		c.stateMu.Unlock()
	}()
	// Subscribe to messages (stream/consumer creation handled by pubsub.Consumer)
	msgCh, err := c.consumer.Subscribe(runCtx)
	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}
	c.stateMu.Lock()
	c.messages = msgCh
	c.stateMu.Unlock()

	// Initialize Worker Pool
	c.workerChans = make([]chan pubsub.Message, c.numWorkers)
	for i := 0; i < c.numWorkers; i++ {
		c.workerChans[i] = make(chan pubsub.Message, c.channelBufSize)
		c.wg.Add(1)
		go c.workerLoop(runCtx, i)
	}

	slog.Info("Trigger Consumer started, waiting for messages", "num_workers", c.numWorkers)

consume:
	for {
		select {
		case <-runCtx.Done():
			break consume
		case msg, ok := <-msgCh:
			if !ok {
				if runCtx.Err() == nil {
					result = c.consumerError()
					if result == nil {
						result = errors.New("trigger delivery consumer stopped unexpectedly")
					}
				}
				break consume
			}
			c.dispatchMessage(runCtx, msg)
		}
	}
	cancel()

	slog.Info("Stopping Trigger Consumer...")
	c.closing.Store(true)

	// The source closes only after its backend consume loop has stopped.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), c.drainTimeout)
	defer drainCancel()
	result = errors.Join(result, c.drainMessages(drainCtx, msgCh))
	c.waitForDrain(drainCtx)

	for _, ch := range c.workerChans {
		close(ch)
	}

	done := make(chan struct{})
	c.stateMu.Lock()
	c.workersDone = done
	c.stateMu.Unlock()
	go func() {
		c.wg.Wait()
		close(done)
	}()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), c.shutdownTimeout)
	defer shutdownCancel()

	select {
	case <-done:
		slog.Info("All workers stopped gracefully")
	case <-shutdownCtx.Done():
		result = errors.Join(result, errors.New("trigger delivery worker shutdown timed out"))
	}

	return result
}

// Shutdown confirms source and worker settlement after Start, including a timed-out shutdown.
// A failed attempt retains the owned lifecycle state for a later bounded retry.
func (c *natsConsumer) Shutdown(ctx context.Context) error {
	c.stateMu.RLock()
	cancel, started, gate := c.runCancel, c.startDone, c.settlementGate
	c.stateMu.RUnlock()
	if started == nil {
		return nil
	}
	cancel()
	select {
	case <-started:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	c.stateMu.RLock()
	messages, workersDone := c.messages, c.workersDone
	c.stateMu.RUnlock()
	if messages != nil {
		if err := c.drainMessages(ctx, messages); err != nil {
			return err
		}
	}
	if workersDone != nil {
		select {
		case <-workersDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *natsConsumer) drainMessages(ctx context.Context, messages <-chan pubsub.Message) error {
	var result error
	for {
		select {
		case msg, ok := <-messages:
			if !ok {
				return result
			}
			result = errors.Join(result, msg.Nak())
		case <-ctx.Done():
			return errors.Join(result, fmt.Errorf("trigger delivery consumer shutdown timed out: %w", ctx.Err()))
		}
	}
}

func (c *natsConsumer) consumerError() error {
	if source, ok := c.consumer.(pubsub.ConsumerState); ok {
		return source.Err()
	}
	return nil
}

func (c *natsConsumer) Readiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.stateMu.RLock()
	running, runCtx, terminal := c.running, c.runCtx, c.terminal
	c.stateMu.RUnlock()
	if terminal != nil {
		return terminal
	}
	if !running {
		return errors.New("trigger delivery pool is not active")
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	if c.closing.Load() || c.activeWorkers.Load() != int32(c.numWorkers) {
		return errors.New("trigger delivery pool is not active")
	}
	if source, ok := c.consumer.(pubsub.ConsumerState); ok {
		if err := source.Err(); err != nil {
			return err
		}
		if source.Ready() {
			return nil
		}
	}
	return errors.New("trigger delivery consumer is not active")
}

func (c *natsConsumer) TerminalError() error {
	c.stateMu.RLock()
	terminal := c.terminal
	c.stateMu.RUnlock()
	if terminal != nil {
		return terminal
	}
	return c.consumerError()
}

// waitForDrain waits for all in-flight dispatch() calls to complete.
func (c *natsConsumer) waitForDrain(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			remaining := c.inFlightCount.Load()
			if remaining > 0 {
				slog.Warn("Drain timeout, messages still in-flight", "remaining", remaining)
			}
			return
		case <-ticker.C:
			if c.inFlightCount.Load() == 0 {
				slog.Info("All in-flight messages drained")
				return
			}
		}
	}
}

func (c *natsConsumer) dispatch(msg pubsub.Message) {
	c.dispatchMessage(context.Background(), msg)
}

func (c *natsConsumer) dispatchMessage(ctx context.Context, msg pubsub.Message) {
	// Track in-flight count for graceful shutdown
	c.inFlightCount.Add(1)
	defer c.inFlightCount.Add(-1)

	// Check if consumer is closing - NAK message for redelivery
	if c.closing.Load() {
		slog.Warn("Consumer is closing, NAK message for redelivery")
		msg.Nak()
		return
	}

	var task types.DeliveryTask
	if err := json.Unmarshal(msg.Data(), &task); err != nil {
		slog.Error("Invalid payload in dispatch", "error", err)
		c.metrics.IncConsumeFailure("unknown", "unknown", "unmarshal_error")
		msg.Term()
		return
	}

	h := fnv.New32a()
	h.Write([]byte(task.Collection))
	h.Write([]byte(task.DocumentID))
	hash := h.Sum32()
	workerIdx := int(hash % uint32(c.numWorkers))

	select {
	case c.workerChans[workerIdx] <- msg:
	case <-ctx.Done():
		msg.Nak()
	}
}

func (c *natsConsumer) workerLoop(ctx context.Context, id int) {
	defer c.wg.Done()
	c.activeWorkers.Add(1)
	defer c.activeWorkers.Add(-1)

	for msg := range c.workerChans[id] {
		if ctx.Err() != nil {
			msg.Nak()
			continue
		}
		if err := c.processMsg(ctx, msg); err != nil {
			if types.IsFatal(err) {
				slog.Error("Fatal error processing message. Terminating.", "worker_id", id, "error", err)
				msg.Term()
				continue
			}
			slog.Error("Failed to process message", "worker_id", id, "error", err)

			md, metaErr := msg.Metadata()
			if metaErr != nil {
				slog.Error("Failed to get message metadata", "error", metaErr)
				msg.Nak()
				continue
			}

			var task types.DeliveryTask
			if jsonErr := json.Unmarshal(msg.Data(), &task); jsonErr != nil {
				slog.Error("Invalid payload for retry check", "error", jsonErr)
				msg.Term()
				continue
			}

			maxAttempts := task.RetryPolicy.MaxAttempts
			if maxAttempts == 0 {
				maxAttempts = 3
			}

			if int(md.NumDelivered) >= maxAttempts {
				slog.Error("Max attempts reached. Terminating.", "max_attempts", maxAttempts, "trigger_id", task.TriggerID)
				msg.Term()
				continue
			}

			attempt := int(md.NumDelivered)
			initialBackoff := time.Duration(task.RetryPolicy.InitialBackoff)
			if initialBackoff == 0 {
				initialBackoff = 1 * time.Second
			}

			backoff := initialBackoff * (1 << (attempt - 1))

			maxBackoff := time.Duration(task.RetryPolicy.MaxBackoff)
			if maxBackoff > 0 && backoff > maxBackoff {
				backoff = maxBackoff
			}

			var retryAfter *types.RetryAfterError
			var hint time.Duration
			if errors.As(err, &retryAfter) {
				hint = retryAfter.Delay
			}
			delay := max(backoff, hint)
			slog.Info("Retrying trigger", "trigger_id", task.TriggerID, "database", task.Database, "collection", task.Collection, "document_id", task.DocumentID, "lsn", task.LSN, "seq", task.Seq, "backoff", delay, "rule_backoff", backoff, "retry_after", hint, "attempt", attempt+1, "max_attempts", maxAttempts)
			if err := msg.NakWithDelay(delay); err != nil {
				slog.Error("Failed to schedule trigger retry", "trigger_id", task.TriggerID, "database", task.Database, "collection", task.Collection, "document_id", task.DocumentID, "lsn", task.LSN, "seq", task.Seq, "delay", delay, "error", err)
			}
		} else {
			msg.Ack()
		}
	}
}

func (c *natsConsumer) processMsg(ctx context.Context, msg pubsub.Message) error {
	start := time.Now()
	var task types.DeliveryTask
	if err := json.Unmarshal(msg.Data(), &task); err != nil {
		c.metrics.IncConsumeFailure("unknown", "unknown", "unmarshal_error")
		return fmt.Errorf("invalid payload: %w", err)
	}

	slog.Info("Processing trigger task", "trigger_id", task.TriggerID)

	timeout := time.Duration(task.Timeout)
	if timeout == 0 {
		timeout = types.DefaultTaskTimeout
	}
	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	err := c.worker.ProcessTask(taskCtx, &task)
	if err != nil {
		c.metrics.IncConsumeFailure(task.Database, task.Collection, err.Error())
	} else {
		c.metrics.IncConsumeSuccess(task.Database, task.Collection, task.SubjectHashed)
	}
	c.metrics.ObserveConsumeLatency(task.Database, task.Collection, time.Since(start))
	return err
}
