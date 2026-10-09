package memory

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
)

// memoryPublisher implements pubsub.Publisher using an in-memory broker.
type memoryPublisher struct {
	engine *Engine
	broker *broker
	opts   pubsub.PublisherOptions
	closed atomic.Bool
}

// Publish sends a message to the specified subject.
func (p *memoryPublisher) Publish(ctx context.Context, subject string, data []byte) error {
	if p.closed.Load() {
		return ErrEngineClosed
	}

	start := time.Now()

	fullSubject := subject
	if p.opts.SubjectPrefix != "" {
		fullSubject = p.opts.SubjectPrefix + "." + subject
	}

	err := p.broker.publish(ctx, fullSubject, data)

	if p.opts.OnPublish != nil {
		p.opts.OnPublish(fullSubject, err, time.Since(start))
	}

	return err
}

// Close releases resources.
func (p *memoryPublisher) Close() error {
	p.closed.Store(true)
	return nil
}
