package nats

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// JetStream defines the minimal JetStream interface needed by this package.
// The full jetstream.JetStream interface satisfies this interface.
type JetStream interface {
	CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error)
	CreateOrUpdateConsumer(ctx context.Context, stream string, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error)
	Publish(ctx context.Context, subject string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

// NewJetStream creates a JetStream instance from a NATS connection.
func NewJetStream(nc *nats.Conn) (JetStream, error) {
	if nc == nil {
		return nil, fmt.Errorf("nats connection cannot be nil")
	}
	return jetstream.New(nc)
}
