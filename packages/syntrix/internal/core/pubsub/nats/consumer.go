package nats

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"
	"github.com/nats-io/nats.go/jetstream"
)

type jetStreamConsumer struct {
	js       JetStream
	binding  pubsubconfig.BrokerBinding
	opts     pubsub.ConsumerOptions
	mu       sync.Mutex
	running  bool
	terminal error
	ready    atomic.Bool
}

func NewConsumer(ctx context.Context, js JetStream, binding pubsubconfig.BrokerBinding, opts pubsub.ConsumerOptions) (pubsub.Consumer, error) {
	if opts.StreamName != binding.StreamName || opts.ConsumerName != binding.ConsumerName || opts.FilterSubject != binding.SubjectPrefix+".>" || opts.Storage != bindingStorage(binding) {
		return nil, errors.New("consumer options conflict with broker binding")
	}
	if opts.ChannelBufSize <= 0 {
		return nil, errors.New("consumer channel buffer size must be positive")
	}
	if _, err := attach(ctx, js, binding, true); err != nil {
		return nil, err
	}
	return &jetStreamConsumer{js: js, binding: binding, opts: opts}, nil
}
func (c *jetStreamConsumer) Ready() bool { return c.ready.Load() }
func (c *jetStreamConsumer) Err() error  { c.mu.Lock(); defer c.mu.Unlock(); return c.terminal }
func (c *jetStreamConsumer) Subscribe(ctx context.Context) (<-chan pubsub.Message, error) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return nil, errors.New("consumer subscription already active")
	}
	c.running, c.terminal = true, nil
	c.mu.Unlock()
	consumer, err := attach(ctx, c.js, c.binding, false)
	if err != nil {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
		return nil, err
	}
	msgCh := make(chan pubsub.Message, c.opts.ChannelBufSize)
	consumeCtx, cancel := context.WithCancel(ctx)
	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		select {
		case msgCh <- WrapMessage(msg):
		case <-consumeCtx.Done():
			_ = msg.Nak()
		}
	}, jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
		if errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, jetstream.ErrBadRequest) {
			c.mu.Lock()
			c.terminal = &OperationError{Operation: "consume", Cause: err}
			c.mu.Unlock()
			c.ready.Store(false)
			cancel()
		}
	}))
	if err != nil {
		cancel()
		close(msgCh)
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
		return nil, &OperationError{Operation: "consume startup", Cause: err}
	}
	c.mu.Lock()
	if c.terminal == nil && consumeCtx.Err() == nil {
		c.ready.Store(true)
	}
	c.mu.Unlock()
	closed := cc.Closed()
	go func() {
		select {
		case <-consumeCtx.Done():
		case <-closed:
			if ctx.Err() == nil {
				c.mu.Lock()
				if c.terminal == nil {
					c.terminal = errors.New("NATS consume loop stopped unexpectedly")
				}
				c.mu.Unlock()
			}
		}
		c.ready.Store(false)
		cancel()
		cc.Stop()
		<-closed
		close(msgCh)
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()
	return msgCh, nil
}
