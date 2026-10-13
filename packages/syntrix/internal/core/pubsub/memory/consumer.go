package memory

import (
	"context"
	"sync"

	"github.com/codetreker/syntrix/internal/core/pubsub"
)

// memoryConsumer implements pubsub.Consumer using an in-memory broker.
type memoryConsumer struct {
	engine   *Engine
	broker   *broker
	opts     pubsub.ConsumerOptions
	mu       sync.RWMutex
	ctx      context.Context
	terminal error
}

// Subscribe starts consuming messages and returns a channel.
func (c *memoryConsumer) Subscribe(ctx context.Context) (<-chan pubsub.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
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

	c.ctx, c.terminal = ctx, nil
	go func() {
		select {
		case <-ctx.Done():
		case <-c.broker.done:
			c.mu.Lock()
			c.terminal = ErrEngineClosed
			c.mu.Unlock()
		}
		unsubscribe()
	}()

	return msgCh, nil
}

func (c *memoryConsumer) Ready() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ctx != nil && c.ctx.Err() == nil && !c.engine.IsClosed()
}
func (c *memoryConsumer) Err() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.terminal != nil {
		return c.terminal
	}
	if c.ctx != nil && c.ctx.Err() == nil && c.engine.IsClosed() {
		return ErrEngineClosed
	}
	return nil
}
