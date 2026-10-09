package memory

import (
	"context"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
)

// memoryMessage implements pubsub.Message for in-memory delivery.
type memoryMessage struct {
	data         []byte
	subject      string
	timestamp    time.Time
	numDelivered uint64

	// For redelivery on Nak
	engine       *Engine
	redeliveryCh chan pubsub.Message
	ctx          context.Context

	mu     sync.Mutex
	acked  bool
	naked  bool
	termed bool
}

// Data returns the raw message payload.
func (m *memoryMessage) Data() []byte {
	return m.data
}

// Subject returns the message subject/topic.
func (m *memoryMessage) Subject() string {
	return m.subject
}

// Ack acknowledges successful processing.
func (m *memoryMessage) Ack() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.acked || m.naked || m.termed {
		return nil // Idempotent
	}
	m.acked = true
	return nil
}

// Nak signals processing failure and requeues immediately (non-blocking).
// If the channel is full or closed, the message is dropped.
func (m *memoryMessage) Nak() error {
	m.mu.Lock()
	if m.acked || m.termed || m.naked {
		m.mu.Unlock()
		return nil
	}
	m.naked = true
	m.numDelivered++
	m.mu.Unlock()

	// Non-blocking send to allow immediate requeue
	// Use recover to handle closed channel gracefully
	defer func() {
		recover() // Ignore panic from send on closed channel
	}()

	select {
	case m.redeliveryCh <- m:
	case <-m.ctx.Done():
	default:
		// Channel full, drop (avoid blocking)
	}
	return nil
}

// NakWithDelay requeues the message after a delay.
func (m *memoryMessage) NakWithDelay(delay time.Duration) error {
	m.mu.Lock()
	if m.acked || m.termed || m.naked {
		m.mu.Unlock()
		return nil
	}
	m.naked = true
	m.numDelivered++
	m.mu.Unlock()

	time.AfterFunc(delay, func() {
		// Check if engine is closed
		if m.engine.IsClosed() {
			return
		}

		// Check if context is cancelled
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		// Reset naked flag for redelivery
		m.mu.Lock()
		m.naked = false
		m.mu.Unlock()

		// Attempt requeue (blocking with context)
		select {
		case m.redeliveryCh <- m:
		case <-m.ctx.Done():
		}
	})
	return nil
}

// Term terminates the message (no redelivery).
func (m *memoryMessage) Term() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.acked || m.naked || m.termed {
		return nil
	}
	m.termed = true
	return nil
}

// Metadata returns delivery metadata.
func (m *memoryMessage) Metadata() (pubsub.MessageMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return pubsub.MessageMetadata{
		NumDelivered: m.numDelivered,
		Timestamp:    m.timestamp,
		Subject:      m.subject,
		Stream:       "", // Not applicable for memory
		Consumer:     "", // Not applicable for memory
	}, nil
}
