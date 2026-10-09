package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
)

// broker manages in-memory message routing. Not exported.
type broker struct {
	engine        *Engine
	mu            sync.RWMutex
	subscriptions map[string]*subscription
	closed        atomic.Bool
}

// subscription represents a single consumer's subscription.
type subscription struct {
	pattern    string
	msgCh      chan pubsub.Message
	ctx        context.Context
	cancelFunc context.CancelFunc
}

// newBroker creates a new broker with a reference to the parent engine.
func newBroker(engine *Engine) *broker {
	return &broker{
		engine:        engine,
		subscriptions: make(map[string]*subscription),
	}
}

// publish sends a message to all matching subscriptions.
func (b *broker) publish(ctx context.Context, subject string, data []byte) error {
	if b.closed.Load() {
		return ErrEngineClosed
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	for pattern, sub := range b.subscriptions {
		if matchSubject(pattern, subject) {
			msg := &memoryMessage{
				data:         data,
				subject:      subject,
				timestamp:    time.Now(),
				numDelivered: 1,
				engine:       b.engine,
				redeliveryCh: sub.msgCh,
				ctx:          sub.ctx,
			}
			select {
			case sub.msgCh <- msg:
			case <-ctx.Done():
				return ctx.Err()
			case <-sub.ctx.Done():
				// Subscription cancelled, skip
			}
		}
	}
	return nil
}

// subscribe creates a subscription for the given pattern.
// Returns the message channel, an unsubscribe function, and any error.
func (b *broker) subscribe(ctx context.Context, pattern string, bufSize int) (<-chan pubsub.Message, func(), error) {
	if b.closed.Load() {
		return nil, nil, ErrEngineClosed
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.subscriptions[pattern] != nil {
		return nil, nil, ErrPatternSubscribed
	}

	subCtx, cancel := context.WithCancel(ctx)
	msgCh := make(chan pubsub.Message, bufSize)

	sub := &subscription{
		pattern:    pattern,
		msgCh:      msgCh,
		ctx:        subCtx,
		cancelFunc: cancel,
	}
	b.subscriptions[pattern] = sub

	unsubscribe := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.subscriptions[pattern] == sub {
			delete(b.subscriptions, pattern)
			cancel()
			close(msgCh)
		}
	}

	return msgCh, unsubscribe, nil
}

// close shuts down the broker and all subscriptions.
func (b *broker) close() error {
	if b.closed.Swap(true) {
		return nil // Already closed
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for _, sub := range b.subscriptions {
		sub.cancelFunc()
		close(sub.msgCh)
	}
	b.subscriptions = nil
	return nil
}

// isClosed returns true if the broker is closed.
func (b *broker) isClosed() bool {
	return b.closed.Load()
}
