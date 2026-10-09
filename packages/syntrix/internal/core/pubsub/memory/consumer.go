package memory

import (
	"context"

	"github.com/codetreker/syntrix/internal/core/pubsub"
)

// memoryConsumer implements pubsub.Consumer using an in-memory broker.
type memoryConsumer struct {
	engine *Engine
	broker *broker
	opts   pubsub.ConsumerOptions
}

// Subscribe starts consuming messages and returns a channel.
func (c *memoryConsumer) Subscribe(ctx context.Context) (<-chan pubsub.Message, error) {
	if c.engine.IsClosed() {
		return nil, ErrEngineClosed
	}

	pattern := c.opts.FilterSubject
	if pattern == "" {
		if c.opts.StreamName != "" {
			pattern = c.opts.StreamName + ".>"
		} else {
			pattern = ">"
		}
	}

	bufSize := c.opts.ChannelBufSize
	if bufSize <= 0 {
		bufSize = pubsub.DefaultConsumerOptions().ChannelBufSize
	}

	msgCh, unsubscribe, err := c.broker.subscribe(ctx, pattern, bufSize)
	if err != nil {
		return nil, err
	}

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()

	return msgCh, nil
}
