package pubsub

import (
	"context"
	"io"
)

// Provider provides factory methods for creating publishers and consumers.
// This interface abstracts the underlying message broker (NATS, in-memory, etc.)
// allowing different implementations to be swapped transparently.
type Provider interface {
	io.Closer

	// NewPublisher creates a new Publisher with the given options.
	NewPublisher(opts PublisherOptions) (Publisher, error)

	// NewConsumer creates a new Consumer with the given options.
	NewConsumer(opts ConsumerOptions) (Consumer, error)
}

// Connectable is an optional interface for providers that need to establish
// a connection before they can be used. Memory-based providers typically
// don't implement this interface.
type Connectable interface {
	Connect(ctx context.Context) error
}
