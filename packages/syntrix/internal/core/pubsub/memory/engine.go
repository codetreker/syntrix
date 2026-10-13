package memory

import (
	"context"
	"github.com/codetreker/syntrix/internal/core/pubsub"
)

// Compile-time check that Engine implements pubsub.Provider
var _ pubsub.Provider = (*Engine)(nil)

// Engine provides the public API for in-memory pubsub.
// It mirrors the NATS JetStream interface for consistent usage.
type Engine struct {
	broker *broker
}

// New creates a new in-memory pubsub engine.
func New() *Engine {
	e := &Engine{}
	e.broker = newBroker(e)
	return e
}

// NewPublisher creates a new in-memory Publisher.
func (e *Engine) NewPublisher(ctx context.Context, opts pubsub.PublisherOptions) (pubsub.Publisher, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.IsClosed() {
		return nil, ErrEngineClosed
	}
	return &memoryPublisher{
		engine: e,
		broker: e.broker,
		opts:   opts,
	}, nil
}

// NewConsumer creates a new in-memory Consumer.
func (e *Engine) NewConsumer(ctx context.Context, opts pubsub.ConsumerOptions) (pubsub.Consumer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.IsClosed() {
		return nil, ErrEngineClosed
	}
	return &memoryConsumer{
		engine: e,
		broker: e.broker,
		opts:   opts,
	}, nil
}

// Close shuts down the engine and all subscriptions.
func (e *Engine) Close() error {
	return e.broker.close()
}

// IsClosed returns true if the engine is closed.
func (e *Engine) IsClosed() bool {
	return e.broker.isClosed()
}
