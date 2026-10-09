package nats

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/nats-io/nats.go/jetstream"
)

// jetStreamConsumer implements pubsub.Consumer using NATS JetStream.
type jetStreamConsumer struct {
	js   JetStream
	opts pubsub.ConsumerOptions
}

// NewConsumer creates a new Consumer backed by NATS JetStream.
func NewConsumer(js JetStream, opts pubsub.ConsumerOptions) (pubsub.Consumer, error) {
	if js == nil {
		return nil, fmt.Errorf("jetstream cannot be nil")
	}

	// Apply defaults
	if opts.ChannelBufSize <= 0 {
		opts.ChannelBufSize = pubsub.DefaultConsumerOptions().ChannelBufSize
	}
	if opts.StreamName == "" {
		return nil, fmt.Errorf("stream name is required")
	}

	return &jetStreamConsumer{js: js, opts: opts}, nil
}

// Subscribe starts consuming messages and returns a channel.
func (c *jetStreamConsumer) Subscribe(ctx context.Context) (<-chan pubsub.Message, error) {
	// Ensure stream exists
	filterSubject := c.opts.FilterSubject
	if filterSubject == "" {
		filterSubject = c.opts.StreamName + ".>"
	}

	storage := jetstream.MemoryStorage
	if c.opts.Storage == pubsub.FileStorage {
		storage = jetstream.FileStorage
	}

	_, err := c.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     c.opts.StreamName,
		Subjects: []string{filterSubject},
		Storage:  storage,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to ensure stream: %w", err)
	}

	// Create durable consumer
	consumerName := c.opts.ConsumerName
	if consumerName == "" {
		consumerName = "consumer"
	}

	consumer, err := c.js.CreateOrUpdateConsumer(ctx, c.opts.StreamName, jetstream.ConsumerConfig{
		Durable:       consumerName,
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: filterSubject,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create consumer: %w", err)
	}

	// Create message channel
	msgCh := make(chan pubsub.Message, c.opts.ChannelBufSize)

	// Track if we're closing to avoid sending to closed channel
	var closing atomic.Bool

	// Start consuming
	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		if closing.Load() {
			msg.Nak()
			return
		}
		select {
		case msgCh <- WrapMessage(msg):
		case <-ctx.Done():
			msg.Nak()
		}
	})
	if err != nil {
		close(msgCh)
		return nil, fmt.Errorf("failed to start consumer: %w", err)
	}

	log.Printf("[pubsub] Consumer subscribed, stream=%s", c.opts.StreamName)

	// Goroutine to handle shutdown
	go func() {
		<-ctx.Done()
		log.Println("[pubsub] Stopping consumer...")
		closing.Store(true)
		cc.Stop()
		close(msgCh)
		log.Println("[pubsub] Consumer stopped")
	}()

	return msgCh, nil
}
