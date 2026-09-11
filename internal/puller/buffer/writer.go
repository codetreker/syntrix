// Package buffer provides event buffering with PebbleDB persistence.
package buffer

import (
	"context"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"go.mongodb.org/mongo-driver/bson"
)

type writeRequest struct {
	key   []byte
	value []byte
	token bson.Raw
	event *events.StoreChangeEvent
}

// Write queues an event and its checkpoint, waiting for capacity until ctx is canceled.
func (b *Buffer) Write(ctx context.Context, evt *events.StoreChangeEvent, token bson.Raw) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		if b.closed {
			if b.failure != nil {
				return b.failure
			}
			return fmt.Errorf("buffer is closed")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(token) == 0 {
			return fmt.Errorf("checkpoint token is required")
		}
		if len(b.pending)+len(b.flushing) < b.queueSize {
			break
		}
		capacityCh := b.capacityCh
		b.mu.Unlock()
		select {
		case <-capacityCh:
		case <-b.closeCh:
		case <-ctx.Done():
		}
		b.mu.Lock()
	}

	key := []byte(evt.BufferKey())
	value, err := events.MarshalEvent(evt)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	b.pending = append(b.pending, &writeRequest{
		key:   key,
		value: value,
		token: append([]byte(nil), token...),
		event: evt,
	})
	if len(b.pending) >= b.batchSize || len(b.pending)+len(b.flushing) >= b.queueSize {
		select {
		case b.notifyCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (b *Buffer) applyBatch(apply func(batch pebbleBatch) error) error {
	batch := b.newBatch()
	defer batch.Close()

	if err := apply(batch); err != nil {
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit batch: %w", err)
	}

	return nil
}

func (b *Buffer) startBatcher() {
	b.batcherWG.Add(1)
	go b.runBatcher()
}

func (b *Buffer) runBatcher() {
	defer b.batcherWG.Done()

	ticker := time.NewTicker(b.batchInterval)
	defer ticker.Stop()

	flush := func() error {
		// Take the oldest capped batch while keeping it visible to readers.
		b.mu.Lock()
		if len(b.pending) == 0 {
			b.mu.Unlock()
			return nil
		}
		count := min(len(b.pending), b.batchSize)
		b.flushing = b.pending[:count:count]
		b.pending = b.pending[count:]
		if len(b.pending) == 0 {
			b.pending = nil
		}
		b.mu.Unlock()

		batch := b.newBatch()
		var commitErr error
		var checkpointToken []byte

		for _, req := range b.flushing {
			if err := batch.Set(req.key, req.value, pebble.Sync); err != nil {
				commitErr = fmt.Errorf("failed to batch write event: %w", err)
				break
			}
			if req.token != nil {
				checkpointToken = append([]byte(nil), req.token...)
			}
		}

		if commitErr == nil && checkpointToken != nil {
			if err := batch.Set(checkpointKeyBytes, checkpointToken, pebble.Sync); err != nil {
				commitErr = fmt.Errorf("failed to batch write checkpoint: %w", err)
			}
		}

		if commitErr == nil {
			if err := batch.Commit(pebble.Sync); err != nil {
				commitErr = fmt.Errorf("failed to commit batch: %w", err)
			}
		}

		if err := batch.Close(); err != nil && commitErr == nil {
			commitErr = fmt.Errorf("failed to close batch: %w", err)
		}

		// Clear flushing queue
		b.mu.Lock()
		clear(b.flushing)
		b.flushing = nil

		if commitErr != nil {
			b.failure = commitErr
			if !b.closed {
				b.closed = true
				// Note: closeCh might be already closed if Close() was called
				select {
				case <-b.closeCh:
				default:
					close(b.closeCh)
				}
			}
			b.mu.Unlock()
			b.logger.Error("failed to flush batch, stopping batcher", "error", commitErr)
			return commitErr
		}
		close(b.capacityCh)
		b.capacityCh = make(chan struct{})
		b.mu.Unlock()
		return nil
	}

	for {
		b.mu.RLock()
		closed := b.closed
		pending := len(b.pending)
		b.mu.RUnlock()
		if closed && pending == 0 {
			return
		}
		if !closed && pending < b.batchSize && pending < b.queueSize {
			select {
			case <-b.notifyCh:
			case <-ticker.C:
			case <-b.closeCh:
			}
		}
		if err := flush(); err != nil {
			return
		}
	}
}

// Flush waits until all admitted events and their raw checkpoints are durable.
// The capture goroutine must remain the sole writer when establishing a boundary.
func (b *Buffer) Flush(ctx context.Context) error {
	for {
		b.mu.RLock()
		pending := len(b.pending) + len(b.flushing)
		failed, closed, capacity := b.failure, b.closed, b.capacityCh
		b.mu.RUnlock()
		if failed != nil {
			return failed
		}
		if closed {
			return fmt.Errorf("buffer is closed")
		}
		if pending == 0 {
			return nil
		}
		select {
		case b.notifyCh <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-b.closeCh:
		case <-capacity:
		}
	}
}
