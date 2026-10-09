package nats

import (
	"context"
	"fmt"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/nats-io/nats.go/jetstream"
)

// jetStreamPublisher implements pubsub.Publisher using NATS JetStream.
type jetStreamPublisher struct {
	js   JetStream
	opts pubsub.PublisherOptions
}

// NewPublisher creates a new Publisher backed by NATS JetStream.
func NewPublisher(js JetStream, opts pubsub.PublisherOptions) (pubsub.Publisher, error) {
	if js == nil {
		return nil, fmt.Errorf("jetstream cannot be nil")
	}

	// Ensure stream exists
	if opts.StreamName != "" {
		subjects := []string{opts.StreamName + ".>"}
		if opts.SubjectPrefix != "" && opts.SubjectPrefix != opts.StreamName {
			subjects = []string{opts.SubjectPrefix + ".>"}
		}

		storage := jetstream.MemoryStorage
		if opts.Storage == pubsub.FileStorage {
			storage = jetstream.FileStorage
		}

		_, err := js.CreateOrUpdateStream(context.Background(), jetstream.StreamConfig{
			Name:     opts.StreamName,
			Subjects: subjects,
			Storage:  storage,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to ensure stream: %w", err)
		}
	}

	return &jetStreamPublisher{js: js, opts: opts}, nil
}

// Publish sends a message to the specified subject.
func (p *jetStreamPublisher) Publish(ctx context.Context, subject string, data []byte) error {
	start := time.Now()

	fullSubject := subject
	if p.opts.SubjectPrefix != "" {
		fullSubject = p.opts.SubjectPrefix + "." + subject
	}

	var publishOpts []jetstream.PublishOpt
	if p.opts.RetryAttempts > 0 {
		publishOpts = append(publishOpts, jetstream.WithRetryAttempts(p.opts.RetryAttempts))
	}

	_, err := p.js.Publish(ctx, fullSubject, data, publishOpts...)

	if p.opts.OnPublish != nil {
		p.opts.OnPublish(fullSubject, err, time.Since(start))
	}

	if err != nil {
		return fmt.Errorf("failed to publish to %s: %w", fullSubject, err)
	}

	return nil
}

// Close releases resources.
func (p *jetStreamPublisher) Close() error {
	// JetStream doesn't need explicit close
	return nil
}
