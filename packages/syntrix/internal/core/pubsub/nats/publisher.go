package nats

import (
	"context"
	"errors"
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"
	"github.com/nats-io/nats.go/jetstream"
)

type jetStreamPublisher struct {
	js   JetStream
	opts pubsub.PublisherOptions
}

func NewPublisher(ctx context.Context, js JetStream, binding pubsubconfig.BrokerBinding, opts pubsub.PublisherOptions) (pubsub.Publisher, error) {
	if opts.StreamName != binding.StreamName || opts.SubjectPrefix != binding.SubjectPrefix || opts.Storage != bindingStorage(binding) {
		return nil, errors.New("publisher options conflict with broker binding")
	}
	if opts.RetryAttempts < 0 {
		return nil, errors.New("publisher retry attempts must not be negative")
	}
	if _, err := attach(ctx, js, binding, true); err != nil {
		return nil, err
	}
	return &jetStreamPublisher{js: js, opts: opts}, nil
}
func bindingStorage(binding pubsubconfig.BrokerBinding) pubsub.StorageType {
	if binding.Policy.Storage == "file" {
		return pubsub.FileStorage
	}
	return pubsub.MemoryStorage
}
func (p *jetStreamPublisher) Publish(ctx context.Context, subject string, data []byte) error {
	start := time.Now()
	fullSubject := p.opts.SubjectPrefix + "." + subject
	var options []jetstream.PublishOpt
	if p.opts.RetryAttempts > 0 {
		options = append(options, jetstream.WithRetryAttempts(p.opts.RetryAttempts))
	}
	_, err := p.js.Publish(ctx, fullSubject, data, options...)
	if err != nil {
		err = &OperationError{Operation: "publish", Cause: err}
	}
	if p.opts.OnPublish != nil {
		p.opts.OnPublish(fullSubject, err, time.Since(start))
	}
	return err
}
func (p *jetStreamPublisher) Close() error { return nil }
